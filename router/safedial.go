package router

import (
	"context"
	"fmt"
	"net"
)

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
