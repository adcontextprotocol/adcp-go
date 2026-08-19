// Package uid2 is a thin HTTP client for a UID2 / EUID operator adapter
// that resolves an encrypted advertising token to the underlying raw ID
// (the stable 32-byte SHA-256 the buyer-side stores key on).
//
// The UID2 wire form is the operator-issued encrypted advertising
// token — a ~180–250-char base64url-alphabet string that is
// case-sensitive per the UID2 spec at
// https://unifiedid.com/docs/getting-started/gs-normalization-encoding.
// The underlying raw ID (the 32-byte SHA-256 of the normalized email
// or phone) is a separate encoding — standard base64 with padding —
// which this client returns as raw bytes.
// Recovering the raw ID requires decrypting the token with per-operator
// key material — a process usually done via IABTechLab's server-side
// SDKs (Java, Python, .NET, C++). No official Go SDK is published, so
// this package instead speaks HTTP to a small operator-adapter service
// the deployer runs alongside the identity agent. The adapter uses one
// of the official SDKs (or a bespoke integration) to perform the
// decrypt and returns the raw ID bytes over a simple JSON contract.
//
// EUID has an identical wire shape and identical decrypt semantics —
// only the operator endpoint and the credentials differ. This package
// therefore supports both scopes with a single Client type; the caller
// instantiates one Client per scope (typically one for UID2 and one
// for EUID) with the appropriate URL and credentials.
//
// Wire contract:
//
//	POST {URL}
//	Content-Type: application/json
//	Authorization: Bearer <APIKey>
//	X-UID2-Client-Secret: <ClientSecret>
//
//	{"token": "<encrypted advertising token>"}
//
// Successful response (HTTP 200):
//
//	{"raw_id": "<standard-base64 encoding of the 32-byte raw ID>"}
//
// Miss responses — user opted out, token expired, token not decryptable
// with the configured operator keys — are surfaced via HTTP 404 or an
// HTTP 200 with an empty `raw_id`. Both collapse to ErrNoMapping.
//
// Design notes:
//
//   - Constructor-based (no package init / global state). Tests
//     instantiate their own Client against an httptest server.
//   - Caller supplies context.Context per request so the identity-match
//     agent can apply its per-request deadline.
//   - One shot, no retries. The 40ms request budget doesn't accommodate
//     a retry chain; the client takes one shot and any failure is
//     reported back so the decoder can silent-drop just this identity.
//   - Case-sensitive throughout: the token is transmitted verbatim; the
//     raw ID is returned as its 32 raw bytes so downstream stores key
//     on the canonical binary form (Canonical(bytes) then produces
//     lowercase-hex — matching ExposureLog.user_token).
//   - No secret material appears in error text or log output. The API
//     key and client secret are held on the Client and only travel in
//     outbound request headers.
package uid2

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Default per-call timeouts are sized to fit the identity-match agent's
// 40ms per-request budget. UID2 operator round-trips are typically
// slower than a colocated LiveRamp sidecar because the adapter must
// perform an AES decrypt (and may itself talk to a remote operator to
// refresh keys), so we default to a larger slice of the budget than
// LiveRamp does — but still well below the 40ms ceiling so the rest of
// the agent's request path has room to breathe. Deployers with a
// tighter latency SLO should set UID2_OPERATOR_TIMEOUT (or the EUID
// equivalent) explicitly.
//
// Retry policy: none. Same rationale as LiveRamp — the per-request
// budget doesn't accommodate retries; treat any failure as a drop for
// just this identity.
const (
	defaultTimeout     = 30 * time.Millisecond
	defaultDialTimeout = 10 * time.Millisecond

	// HTTP transport tuning. The operator adapter is a hot-path
	// dependency — Go's default MaxIdleConnsPerHost=2 would cause
	// connection thrash at agent QPS, and the absence of
	// ResponseHeaderTimeout means an adapter that ACKs a connection
	// then hangs costs us the full per-request budget. These values
	// pin the transport to a small, well-behaved keep-alive pool.
	transportMaxIdleConns        = 64
	transportMaxIdleConnsPerHost = 32
	transportIdleConnTimeout     = 90 * time.Second
	transportTLSHandshakeTimeout = 15 * time.Millisecond
	transportResponseHeaderLimit = 30 * time.Millisecond
	transportExpectContinueLimit = 1 * time.Second

	// Header names for the operator-adapter contract. Kept as
	// constants so tests can reference the same values the production
	// path emits.
	headerAuthorization  = "Authorization"
	headerClientSecret   = "X-UID2-Client-Secret" //nolint:gosec // header name, not a secret
	authorizationPrefix  = "Bearer "
	contentTypeHeader    = "Content-Type"
	contentTypeJSON      = "application/json"
	acceptHeader         = "Accept"
	// responseBodyReadCap is generous — the adapter's decryptResponse
	// is tens of bytes at most. The cap is defensive against a
	// misbehaving adapter, not a size hint.
	responseBodyReadCap  = 4 * 1024
	responseBodyDrainCap = 4 * 1024
)

// ErrNoMapping is the sentinel returned by Decrypt when the operator
// adapter reachably responds but cannot decrypt the token to a raw ID.
// Three responses map to this sentinel:
//
//   - HTTP 200 with an empty or absent `raw_id` field: the token was
//     accepted but produced no raw ID (user opted out, token expired).
//   - HTTP 404: the operator knows the token but treats it as
//     permanently unresolvable (expired / revoked). Semantically a
//     miss, not a transport failure — advertising tokens rotate as
//     part of normal UID2 lifecycle.
//   - HTTP 410 Gone: alternate spelling of the "known-but-expired"
//     signal some operator adapters use.
//
// Distinguished from transport / auth / decode errors so callers can
// treat "miss" as a routine outcome and drop just this identity.
var ErrNoMapping = errors.New("uid2: no mapping")

// Config carries the connection parameters for a UID2 / EUID operator
// Client.
//
// URL must be the full operator-adapter endpoint including scheme,
// host, port, and path (e.g. https://uid2-adapter.svc.cluster.local/v2/token/decrypt).
// The Client does no path joining at request time — build the URL
// with the path baked in.
//
// APIKey and ClientSecret are transmitted on every request as
// `Authorization: Bearer <APIKey>` and `X-UID2-Client-Secret:
// <ClientSecret>` respectively. Both are required when URL is set.
// They are never logged and never appear in returned error text.
type Config struct {
	URL          string
	APIKey       string
	ClientSecret string
	Timeout      time.Duration
	DialTimeout  time.Duration

	// HTTPClient is an optional override used by tests to inject an
	// httptest.Server's TLS client. When nil the Client builds its own
	// transport from Timeout / DialTimeout.
	HTTPClient *http.Client
}

// Client resolves an encrypted UID2 (or EUID) advertising token to its
// raw 32-byte ID via the operator adapter described in the package
// comment. A single Client is scoped to one operator endpoint — build
// one for UID2 and (if configured) one for EUID.
type Client struct {
	endpoint     *url.URL
	http         *http.Client
	apiKey       string
	clientSecret string
}

// NewClient validates cfg and constructs a Client. Returns an error
// when URL is empty or unparseable, when the scheme is not http/https,
// or when APIKey or ClientSecret is empty. Defaults are applied for
// unset timeouts.
func NewClient(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, errors.New("uid2: URL is required")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("uid2: parse URL %q: %w", cfg.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("uid2: URL scheme %q must be http or https", u.Scheme)
	}
	if cfg.APIKey == "" {
		return nil, errors.New("uid2: APIKey is required")
	}
	if cfg.ClientSecret == "" {
		return nil, errors.New("uid2: ClientSecret is required")
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
	return &Client{
		endpoint:     u,
		http:         hc,
		apiKey:       cfg.APIKey,
		clientSecret: cfg.ClientSecret,
	}, nil
}

// decryptRequest is the JSON body posted to the operator adapter.
// The field name is stable — changing it silently would rebreak every
// adapter implementation, so a top-level constant lives with the
// package comment as the wire contract anchor.
type decryptRequest struct {
	Token string `json:"token"`
}

// decryptResponse is the JSON body the operator adapter returns on
// HTTP 200. Miss responses are also encoded here with an empty
// `raw_id`.
type decryptResponse struct {
	// RawID is the standard-base64 encoding of the 32-byte raw UID2 /
	// EUID. Empty when the adapter reachably responded but produced
	// no mapping (opt-out / expired). Case-sensitive per spec — the
	// Client decodes it verbatim and never touches case on either
	// the token or the raw bytes.
	RawID string `json:"raw_id"`
}

// Decrypt sends token to the operator adapter and returns the raw ID
// bytes (32 raw bytes; the pre-hex form of the UID2 / EUID). Returns
// ErrNoMapping when the adapter reachably responds but produces no
// mapping (opt-out / expired). Transport, auth, decode, and other
// non-OK-status errors are returned as-is so the caller can decide
// whether to alert.
//
// token is sent verbatim in the request body; the caller must not
// pre-encode it. Case is preserved end to end — the token, the
// base64 raw_id, and the decoded bytes all round-trip without any
// ToLower / ToUpper along the way.
//
// The returned []byte aliases a fresh allocation so the caller may
// keep it beyond the response lifetime. The 32-byte length is
// verified before return; a wrong-sized decode surfaces as an error
// so the identity-agent's per-type size check has something concrete
// to fail against upstream.
func (c *Client) Decrypt(ctx context.Context, token string) ([]byte, error) {
	if token == "" {
		return nil, errors.New("uid2: token must be non-empty")
	}
	body, err := json.Marshal(decryptRequest{Token: token})
	if err != nil {
		// Marshaling a struct with a single string field cannot
		// fail in practice — the branch exists for defensive
		// completeness and to keep the caller from wrapping a nil
		// error case.
		return nil, fmt.Errorf("uid2: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(contentTypeHeader, contentTypeJSON)
	req.Header.Set(acceptHeader, contentTypeJSON)
	req.Header.Set(headerAuthorization, authorizationPrefix+c.apiKey)
	req.Header.Set(headerClientSecret, c.clientSecret)

	// gosec G107 (SSRF): the URL is fixed at construction time from
	// operator configuration (UID2_OPERATOR_URL / EUID_OPERATOR_URL);
	// only the JSON body is influenced by request data, which is the
	// documented adapter contract. The Client is not a generic HTTP
	// fetcher.
	resp, err := c.http.Do(req) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through to body parsing
	case http.StatusNotFound, http.StatusGone:
		// Adapter knows the token but has no mapping (expired /
		// revoked / opted out). Same downstream treatment as the
		// 200-empty-body miss.
		_, _ = io.CopyN(io.Discard, resp.Body, responseBodyDrainCap)
		return nil, ErrNoMapping
	case http.StatusUnauthorized, http.StatusForbidden:
		// Bad credentials or unauthorized adapter — surface as a
		// distinct error so operators see the misconfiguration
		// rather than a silent identity drop. Never echo the
		// response body: an adapter that reflects request state
		// could otherwise expose the API key.
		_, _ = io.CopyN(io.Discard, resp.Body, responseBodyDrainCap)
		return nil, fmt.Errorf("uid2: operator rejected credentials (HTTP %d)", resp.StatusCode)
	default:
		// Drain a bounded prefix so the connection can be reused.
		// A misbehaving adapter shouldn't be allowed to wedge
		// connections by holding the body open.
		_, _ = io.CopyN(io.Discard, resp.Body, responseBodyDrainCap)
		return nil, fmt.Errorf("uid2: operator status %d", resp.StatusCode)
	}

	// Read up to a bounded prefix; a malformed adapter that streams
	// the full internet at us shouldn't be allowed to wedge the pool.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseBodyReadCap))
	if err != nil {
		return nil, fmt.Errorf("uid2: read body: %w", err)
	}
	// Drain up to the bounded prefix so the connection stays reusable.
	// The success path stays symmetrical with the error branches — no
	// path lets a misbehaving adapter stream unbounded bytes at us.
	_, _ = io.CopyN(io.Discard, resp.Body, responseBodyDrainCap)
	if len(raw) == 0 {
		return nil, ErrNoMapping
	}

	var parsed decryptResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("uid2: parse body: %w", err)
	}
	if parsed.RawID == "" {
		return nil, ErrNoMapping
	}

	// Decode the raw ID verbatim — case-preserving. Standard base64
	// with padding is the UID2 canonical shape; a raw-encoding
	// fallback would blur the wire contract.
	decoded, err := base64.StdEncoding.DecodeString(parsed.RawID)
	if err != nil {
		return nil, fmt.Errorf("uid2: decode raw_id: %w", err)
	}
	// The UID2 / EUID raw ID is a SHA-256 of the normalized email or
	// phone: exactly 32 bytes. The identity-agent's per-type size
	// check will also verify this before the token reaches the wire,
	// but rejecting here gives the operator a clearer error path
	// than a downstream size_mismatch counter.
	const rawIDSize = 32
	if len(decoded) != rawIDSize {
		return nil, fmt.Errorf("uid2: raw_id decoded to %d bytes, want %d", len(decoded), rawIDSize)
	}
	return decoded, nil
}
