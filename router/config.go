package router

import (
	"fmt"
	"net"
	"net/url"
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
// (not pointing at localhost, metadata services, or private ranges).
func ValidateProviderEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint URL: %w", err)
	}
	host := u.Hostname()
	if host == "localhost" || host == "" {
		return fmt.Errorf("endpoint must not target localhost: %s", endpoint)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil // hostname, not IP — DNS resolution happens later
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("endpoint must not target local/link-local address: %s", endpoint)
	}
	// Block cloud metadata endpoints (169.254.169.254).
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return fmt.Errorf("endpoint must not target metadata service: %s", endpoint)
	}
	return nil
}
