package main

import "time"

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

	Timeout time.Duration `json:"timeout"`
}

// RouterConfig defines the router's runtime configuration.
type RouterConfig struct {
	ListenAddr string           `json:"listen_addr"`
	Providers  []ProviderConfig `json:"providers"`
}
