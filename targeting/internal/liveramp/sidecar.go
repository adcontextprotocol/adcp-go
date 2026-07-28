// Package liveramp is a thin HTTP client for a LiveRamp mapping
// sidecar. Given a LiveRamp environment identifier ("env" / RampID),
// the sidecar returns the platform-mapped form the rest of the
// identity pipeline consumes.
//
// Design notes:
//
//   - Constructor-based (no package init / global state). Tests
//     instantiate their own Client against an httptest server.
//   - Caller supplies context.Context per request so the identity-match
//     agent can apply its per-request deadline.
package liveramp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Default per-call timeouts are sized to fit the identity-match
// agent's 40ms per-request budget. A LiveRamp call that takes more
// than ~15ms is already starving the rest of the agent's request
// path; the caller should treat that as a miss and drop the
// identity.
//
// Retry policy: none. The 40ms budget doesn't accommodate a retry chain;
// the LiveRamp client takes one shot and any failure is reported back so
// selectEntries can drop just that identity.
const (
	defaultTimeout     = 15 * time.Millisecond
	defaultDialTimeout = 5 * time.Millisecond

	// HTTP transport tuning. The sidecar is a hot-path internal service —
	// Go's default MaxIdleConnsPerHost=2 would cause connection thrash at
	// the agent's QPS, and the absence of ResponseHeaderTimeout means a
	// sidecar that ACKs a connection then hangs costs us the full
	// per-request budget. These values pin the transport to a small,
	// well-behaved keep-alive pool.
	transportMaxIdleConns        = 64
	transportMaxIdleConnsPerHost = 32
	transportIdleConnTimeout     = 90 * time.Second
	transportTLSHandshakeTimeout = 10 * time.Millisecond
	transportResponseHeaderLimit = 15 * time.Millisecond
	transportExpectContinueLimit = 1 * time.Second

	// scope3SeatID is the key inside the per-source mapping object that
	// carries the platform-mapped form.
	scope3SeatID = "Scope3"

	// liverampSource is the source identifier the sidecar publishes for
	// LiveRamp mappings. Other sources may appear in the response and are
	// ignored.
	liverampSource = "liveramp.com"

	// envParam is the query parameter the sidecar expects.
	envParam = "env"
)

// ErrNoMapping is the sentinel returned by MappedID when the sidecar
// reachably responds but has no mapping for the supplied env. Two sidecar
// responses map to this sentinel:
//
//   - 200 with an empty (or Scope3-key-absent) body: the env was accepted but
//     no platform-mapped value is available.
//   - 410 Gone: LiveRamp knows the env but treats it as permanently
//     unresolvable (expired / revoked envelope). Semantically a miss, not a
//     transport failure — envelopes rotate as part of normal cookie lifetime.
//
// Distinguished from transport errors so callers can treat "miss" as a
// routine outcome.
var ErrNoMapping = errors.New("liveramp: no mapping")

// Config carries the connection parameters for a LiveRamp sidecar Client.
//
// URL must be the full sidecar endpoint, including scheme, host, port, and
// path (e.g. https://liveramp-sidecar.svc.cluster.local/v2/map). Build the
// URL with the path baked in so the Client does no joining at request time
// — it only appends ?env=<token>.
type Config struct {
	URL         string
	Timeout     time.Duration
	DialTimeout time.Duration

	// HTTPClient is an optional override used by tests to inject an
	// httptest.Server's TLS client. When nil the Client builds its own
	// transport from Timeout / DialTimeout.
	HTTPClient *http.Client
}

// Client looks up LiveRamp env → platform-mapped value via the
// sidecar.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// NewClient validates cfg and constructs a Client. Returns an error when
// URL is empty or unparseable; defaults are applied for unset timeouts.
func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("liveramp: URL is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("liveramp: parse URL %q: %w", cfg.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("liveramp: URL scheme %q must be http or https", u.Scheme)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = defaultDialTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
		hc = &http.Client{
			Transport: &http.Transport{
				DialContext:           dialer.DialContext,
				MaxIdleConns:          transportMaxIdleConns,
				MaxIdleConnsPerHost:   transportMaxIdleConnsPerHost,
				IdleConnTimeout:       transportIdleConnTimeout,
				TLSHandshakeTimeout:   transportTLSHandshakeTimeout,
				ResponseHeaderTimeout: transportResponseHeaderLimit,
				ExpectContinueTimeout: transportExpectContinueLimit,
				ForceAttemptHTTP2:     true,
			},
			Timeout: timeout,
		}
	}
	return &Client{baseURL: u, http: hc}, nil
}

// mapping is the per-source object inside the sidecar's response
// array.
type mapping struct {
	Source  string            `json:"source"`
	Mapping map[string]string `json:"mapping"`
}

// MappedID looks up env in the sidecar and returns the
// platform-mapped value (the value the sidecar publishes under the
// scope3SeatID wire key).
// Returns ErrNoMapping when the sidecar reachably responds but has no
// mapping — either 200 with an empty/Scope3-key-absent body, or 410 Gone
// for an expired/revoked envelope. Transport, decode, and other non-OK
// status errors are returned as-is so the caller can decide whether to alert.
//
// env is sent unmodified as the ?env= query parameter; the caller is
// responsible for URL-encoding constraints (the underlying net/url machinery
// handles standard percent-encoding, but the caller must not pre-encode).
func (c *Client) MappedID(ctx context.Context, env string) (string, error) {
	if env == "" {
		return "", errors.New("liveramp: env must be non-empty")
	}
	u := *c.baseURL
	q := u.Query()
	q.Set(envParam, env)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}

	// gosec G704 (SSRF): the URL is fixed at construction time from operator
	// configuration (LIVERAMP_SIDECAR_URL); only the ?env= query parameter
	// is influenced by request data, which is the documented sidecar
	// contract. The Client is not a generic HTTP fetcher.
	resp, err := c.http.Do(req) //nolint:gosec

	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusGone {
		// LiveRamp knows the env but the envelope is permanently
		// unresolvable (expired / revoked). Same downstream treatment as
		// the 200-empty-body miss.
		_, _ = io.CopyN(io.Discard, resp.Body, 4*1024)
		return "", ErrNoMapping
	}
	if resp.StatusCode != http.StatusOK {
		// Drain a bounded prefix so the connection can be reused. A misbehaving
		// sidecar shouldn't be allowed to wedge connections by holding the
		// body open, so we cap reads.
		_, _ = io.CopyN(io.Discard, resp.Body, 4*1024)
		return "", fmt.Errorf("liveramp: sidecar status %d", resp.StatusCode)
	}
	// Sidecar contract: 200 + empty body = no mapping. Determined by
	// reading the body — Content-Length is -1 under chunked encoding
	// and a `ContentLength == 0` short-circuit would misread that as
	// "empty".
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("liveramp: read body: %w", err)
	}
	// Drain anything past the read limit so the connection stays
	// reusable. A misbehaving sidecar that streams >64 KiB doesn't get to
	// wedge our keep-alive pool.
	_, _ = io.Copy(io.Discard, resp.Body)
	if len(body) == 0 {
		return "", ErrNoMapping
	}

	var mappings []mapping
	if err := json.Unmarshal(body, &mappings); err != nil {
		return "", fmt.Errorf("liveramp: parse body: %w", err)
	}
	for _, m := range mappings {
		if m.Source != liverampSource {
			continue
		}
		if v, ok := m.Mapping[scope3SeatID]; ok && v != "" {
			return v, nil
		}
	}
	return "", ErrNoMapping
}
