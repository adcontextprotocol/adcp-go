package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// noFollowRedirect is the CheckRedirect function every router-side
// HTTP client (fan-out, health probe, discovery poll) uses. TMP
// forbids redirects on provider endpoints — a 3xx would let a
// provider re-target the signed request body to any host DNS/rebind
// lands on, replaying identity tokens, sealed credentials, and
// artifact bytes there. safeDialContext blocks only private
// destinations; a public attacker-controlled host would still
// resolve, so the redirect refusal is the load-bearing rule.
// Returning ErrUseLastResponse surfaces the 3xx response to the
// caller instead of following it.
func noFollowRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// safeDialContext resolves DNS and validates the resolved IP against private/loopback
// ranges before connecting. This prevents DNS rebinding attacks where a hostname
// passes initial SSRF validation but later resolves to an internal address.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	for _, ip := range ips {
		resolved := ip.IP
		if v4 := resolved.To4(); v4 != nil {
			resolved = v4
		}
		if resolved.IsLoopback() || resolved.IsPrivate() || resolved.IsLinkLocalUnicast() || resolved.IsLinkLocalMulticast() {
			return nil, fmt.Errorf("resolved address %s is not allowed for %s", resolved, host)
		}
	}
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}
