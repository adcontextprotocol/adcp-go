package safefetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestValidateTarget(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://buyer.example.com/.well-known/trust.json", true},
		{"https://buyer.example.com:8443/x", true},
		{"http://buyer.example.com/x", false},
		{"https://user@buyer.example.com/x", false},
		{"https://buyer.example.com/x#frag", false},
		{"https://buyer.example.com/a b", false},
		{"https://127.0.0.1/x", false},
		{"https://[::1]/x", false},
		{"https://10.1.2.3/x", false},
		{"https://169.254.169.254/latest", false},
		{"https://[::ffff:127.0.0.1]/x", false},
		{"https://localhost/x", false},
		{"https://a.localhost/x", false},
		{"https:///x", false},
		{"//buyer.example.com/x", false},
		{"https://8.8.8.8/x", true},
	}
	for _, tc := range cases {
		err := ValidateTarget(tc.url)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateTarget(%q) = %v, want ok=%v", tc.url, err, tc.ok)
		}
	}
}

func TestBlocked(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "172.31.255.255", "192.168.1.1", "100.64.0.1",
		"169.254.1.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "198.18.0.1", "::1", "fe80::1",
		"fc00::1", "fd12::1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "2001:db8::1", "ff02::1"} {
		if !Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s not blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s blocked", s)
		}
	}
}

func TestGetCapsBodyAndNeverFollowsRedirects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 11)))
		case "/redirect":
			http.Redirect(w, r, "/big", http.StatusFound)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// The test server is on loopback, so exercise Get's body handling via a
	// public-looking URL rewritten by the trusted test transport.
	base := srv.URL
	client.Transport = rewriteTransport{base: base, inner: client.Transport}

	resp, err := Get(context.Background(), client, "https://fetch.example.com/ok", Options{MaxBytes: 10})
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("got %v %v", resp, err)
	}
	if _, err := Get(context.Background(), client, "https://fetch.example.com/big", Options{MaxBytes: 10}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big: %v", err)
	}
	resp, err = Get(context.Background(), client, "https://fetch.example.com/redirect", Options{MaxBytes: 1 << 10})
	if err != nil || resp.Status != http.StatusFound {
		t.Fatalf("redirect followed or failed: %v %v", resp, err)
	}
	if _, err := Get(context.Background(), client, "https://127.0.0.1/ok", Options{MaxBytes: 10}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback literal: %v", err)
	}
}

func TestNewClientRefusesRedirectsAndProxies(t *testing.T) {
	c := NewClient()
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("redirects not refused")
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatal("proxy configured")
	}
}

type rewriteTransport struct {
	base  string
	inner http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u := *r.URL
	target, _ := http.NewRequest(r.Method, rt.base+u.Path, nil)
	target.Header = r.Header
	return rt.inner.RoundTrip(target.WithContext(r.Context()))
}
