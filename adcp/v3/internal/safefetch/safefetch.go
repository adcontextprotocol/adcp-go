// Package safefetch performs the bounded, SSRF-validated HTTPS GETs that
// AdCP 3.3 agent identity needs: key directories, origin revocation lists,
// and trust records. Every fetch is HTTPS only, follows no redirects, uses
// no proxy, checks every resolved address before dialing and pins the dial
// to a checked address, and caps the body and the total time.
package safefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// Response is a completed fetch. Body is at most the requested cap.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Options configures one Get.
type Options struct {
	// Accept is sent as the Accept header.
	Accept string
	// MaxBytes caps the body. A larger body is an error. Required.
	MaxBytes int64
	// Header carries extra request headers (for example If-None-Match).
	Header http.Header
}

// ErrBlocked reports a target that fails the SSRF rules.
var ErrBlocked = errors.New("safefetch: target address not allowed")

// ErrTooLarge reports a body over Options.MaxBytes.
var ErrTooLarge = errors.New("safefetch: response body exceeds cap")

// ValidateTarget applies the static half of the AdCP webhook URL validation
// rules: an absolute https URL with a host, no userinfo, no whitespace or
// control characters, no fragment, and, when the host is an IP literal, an
// address outside every blocked range.
func ValidateTarget(rawURL string) error {
	for i := 0; i < len(rawURL); i++ {
		if c := rawURL[i]; c <= 0x20 || c == 0x7f {
			return fmt.Errorf("%w: URL contains whitespace or a control character", ErrBlocked)
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return fmt.Errorf("%w: an absolute https URL is required", ErrBlocked)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo is not allowed", ErrBlocked)
	}
	if u.Fragment != "" || strings.Contains(rawURL, "#") {
		return fmt.Errorf("%w: fragment is not allowed", ErrBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: host is required", ErrBlocked)
	}
	if addr, err := netip.ParseAddr(host); err == nil && Blocked(addr) {
		return fmt.Errorf("%w: address literal in a blocked range", ErrBlocked)
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("%w: localhost is not allowed", ErrBlocked)
	}
	return nil
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4 special-purpose and non-public ranges.
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		// IPv6 special-purpose and non-public ranges.
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64",
		"2001::/23", "2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10",
		"fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Blocked reports whether addr is in a range a hardened fetcher must not
// dial: loopback, private, link-local, CGNAT, documentation, benchmarking,
// multicast, reserved, and the IPv6 translation and ULA ranges. An
// IPv4-mapped IPv6 address is checked as its IPv4 address.
func Blocked(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// NewClient returns an http.Client for identity fetches: SSRF-checked and
// pinned dialing, no proxy, no redirects, 5 s connect and TLS handshake,
// 10 s total.
func NewClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			if len(addrs) == 0 {
				return nil, ErrBlocked
			}
			for _, a := range addrs {
				if Blocked(a) {
					return nil, ErrBlocked
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableCompression:    true,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Get validates rawURL with ValidateTarget and fetches it with client
// (NewClient when nil), returning the status, headers, and a body of at most
// opts.MaxBytes. Redirects are returned as responses, never followed.
func Get(ctx context.Context, client *http.Client, rawURL string, opts Options) (*Response, error) {
	if opts.MaxBytes <= 0 {
		return nil, errors.New("safefetch: MaxBytes is required")
	}
	if err := ValidateTarget(rawURL); err != nil {
		return nil, err
	}
	if client == nil {
		client = NewClient()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range opts.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if opts.Accept != "" {
		req.Header.Set("Accept", opts.Accept)
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req) // #nosec G107 -- target validated by ValidateTarget; dial checked by the client
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, opts.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > opts.MaxBytes {
		return nil, ErrTooLarge
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body}, nil
}
