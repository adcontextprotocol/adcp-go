package router

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ProviderConfig defines a registered TMP provider.
type ProviderConfig struct {
	ID            string   `json:"id"`
	Endpoint      string   `json:"endpoint"`
	ContextMatch  bool     `json:"context_match"`
	IdentityMatch bool     `json:"identity_match"`
	WireFormats   []string `json:"wire_formats"`

	// Provider-side filters — router skips this provider for non-matching requests.
	PropertyIDs        []string `json:"property_ids,omitempty"`         // Only send these (empty = all)
	ExcludePropertyIDs []string `json:"exclude_property_ids,omitempty"` // Never send these
	PropertyTypes      []string `json:"property_types,omitempty"`       // Only these types (empty = all)
	PackageIDs         []string `json:"package_ids,omitempty"`          // Only send these packages (empty = all)

	Timeout time.Duration `json:"timeout"`
}

// RouterConfig defines the router's runtime configuration.
type RouterConfig struct {
	ListenAddr string           `json:"listen_addr"`
	Providers  []ProviderConfig `json:"providers"`
}

// ValidateProviderEndpoint checks that a provider endpoint URL is safe
// (not pointing at localhost, metadata services, or private/RFC-1918 ranges).
func ValidateProviderEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint must use http or https scheme: %s", endpoint)
	}
	if u.Host == "" {
		return fmt.Errorf("endpoint must be an absolute URL with scheme and host: %s", endpoint)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return fmt.Errorf("endpoint must not target localhost: %s", endpoint)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // hostname, not IP — DNS resolution happens later
	}
	// Unwrap IPv4-mapped IPv6 addresses (e.g. ::ffff:192.168.1.1) so that
	// IsPrivate/IsLoopback correctly identify the underlying IPv4 range.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("endpoint must not target local/private address: %s", endpoint)
	}
	return nil
}
