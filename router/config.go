package router

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ProviderStatus is the schema-generated provider lifecycle type.
// Aliased here so the router package can use it without importing tmproto everywhere.
type ProviderStatus = tmproto.ProviderStatus

const (
	ProviderStatusActive   = tmproto.ProviderStatusActive
	ProviderStatusInactive = tmproto.ProviderStatusInactive
	ProviderStatusDraining = tmproto.ProviderStatusDraining
)

// ProviderConfig defines a registered TMP provider.
// Core fields (ID, Endpoint, Status, etc.) align with the schema-generated
// tmproto.ProviderRegistration type. Router-specific fields (PropertyIDs,
// ExcludePropertyIDs, etc.) extend the registration for routing logic.
type ProviderConfig struct {
	ID            string         `json:"id"`
	Endpoint      string         `json:"endpoint"`
	Status        ProviderStatus `json:"status,omitempty"` // default: active
	ContextMatch  bool           `json:"context_match"`
	IdentityMatch bool           `json:"identity_match"`
	WireFormats   []string       `json:"wire_formats"`

	// Provider-side filters — router skips this provider for non-matching requests.
	PropertyIDs        []string `json:"property_ids,omitempty"`         // Match on publisher's property_id slug (empty = all)
	PropertyRIDs       []string `json:"property_rids,omitempty"`        // Match on registry property_rid UUID (empty = all). Populated by discovery from ProviderRegistration.Properties.
	ExcludePropertyIDs []string `json:"exclude_property_ids,omitempty"` // Never send these (matches property_id slug)
	PropertyTypes      []string `json:"property_types,omitempty"`       // Only these types (empty = all)
	PackageIDs         []string `json:"package_ids,omitempty"`          // Only send these packages (empty = all)

	// Identity match routing — required when IdentityMatch is true.
	Countries []string `json:"countries,omitempty"` // ISO 3166-1 alpha-2 codes this provider serves
	UIDTypes  []string `json:"uid_types,omitempty"` // Identity types this provider can resolve

	// TmpxSlots is the provider's registered ordered slot_id list from
	// provider-registration.json's `tmpx_slots`. Used by the router at
	// merge time to enforce the slot-contract MUST from
	// adcontextprotocol/adcp#5971: a provider's emitted `tmpx_chunks`
	// sequence must be an exact non-empty ordered prefix of this list,
	// else the router drops that provider's chunks atomically before
	// forwarding under `tmpx_providers`.
	TmpxSlots []string `json:"tmpx_slots,omitempty"`

	// Verified-identity attestation (experimental, trusted_match.verified_identity).
	// HPKE key ids this provider can open. The router forwards a
	// sealed_credentials[] entry only to the provider whose AudienceKIDs
	// contains the entry's audience_kid — never broadcast — so a network-scoped
	// credential reaches only its owning audience. A provider that declares none
	// receives no sealed credentials.
	AudienceKIDs []string `json:"audience_kids,omitempty"`

	Timeout time.Duration `json:"timeout"`

	// Priority resolves duplicate package_id offers returned by different
	// Context Match providers. Lower values have higher priority; equal values
	// are broken by response arrival order.
	Priority int `json:"priority,omitempty"`
}

// UnmarshalJSON accepts both the impl-internal field names (id, timeout) and
// the schema-aligned spec field names (provider_id, timeout_ms) so a config
// file that mirrors the wire schema in router-architecture.mdx parses without
// silently dropping fields. The schema names take precedence when both appear.
func (p *ProviderConfig) UnmarshalJSON(data []byte) error {
	type providerConfigAlias ProviderConfig
	aux := struct {
		*providerConfigAlias
		ProviderID string `json:"provider_id"`
		TimeoutMs  *int   `json:"timeout_ms"`
	}{providerConfigAlias: (*providerConfigAlias)(p)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.ProviderID != "" {
		p.ID = aux.ProviderID
	}
	if aux.TimeoutMs != nil {
		p.Timeout = time.Duration(*aux.TimeoutMs) * time.Millisecond
	}
	return nil
}

// EffectiveStatus returns the provider's status, defaulting to active when empty.
func (p *ProviderConfig) EffectiveStatus() ProviderStatus {
	if p.Status == "" {
		return ProviderStatusActive
	}
	return p.Status
}

// ValidateProviderConfig checks that a provider registration is valid.
// latencyBudget of 0 disables the timeout check.
func ValidateProviderConfig(p *ProviderConfig, latencyBudget time.Duration) error {
	if p.ID == "" {
		return fmt.Errorf("provider ID must not be empty")
	}
	if len(p.ID) > 128 {
		return fmt.Errorf("provider %q: ID exceeds maximum length of 128", p.ID[:64]+"...")
	}
	for _, c := range p.ID {
		if c < 0x20 || c == 0x7f || c == 0x00 {
			return fmt.Errorf("provider ID contains invalid characters")
		}
	}
	if !p.ContextMatch && !p.IdentityMatch {
		return fmt.Errorf("provider %q: at least one of context_match or identity_match must be true", p.ID)
	}
	if p.IdentityMatch {
		if len(p.Countries) == 0 {
			return fmt.Errorf("provider %q: countries must be non-empty when identity_match is true", p.ID)
		}
		if len(p.UIDTypes) == 0 {
			return fmt.Errorf("provider %q: uid_types must be non-empty when identity_match is true", p.ID)
		}
	}
	if latencyBudget > 0 && p.Timeout > 0 && p.Timeout > latencyBudget {
		return fmt.Errorf("provider %q: timeout %v exceeds latency budget %v", p.ID, p.Timeout, latencyBudget)
	}
	return nil
}

// ProviderConfigFromRegistration converts a schema-generated ProviderRegistration
// (the wire format from discovery endpoints) into a router ProviderConfig.
func ProviderConfigFromRegistration(r *tmproto.ProviderRegistration) ProviderConfig {
	uidTypes := make([]string, len(r.UIDTypes))
	for i, u := range r.UIDTypes {
		uidTypes[i] = string(u)
	}
	return ProviderConfig{
		ID:            r.ProviderID,
		Endpoint:      r.Endpoint,
		Status:        r.Status,
		ContextMatch:  r.ContextMatch,
		IdentityMatch: r.IdentityMatch,
		Countries:     r.Countries,
		UIDTypes:      uidTypes,
		PropertyRIDs:  r.Properties, // Properties in the spec are registry RIDs (UUIDs), not slugs.
		Timeout:       time.Duration(r.TimeoutMs) * time.Millisecond,
		Priority:      r.Priority,
		TmpxSlots:     append([]string(nil), r.TmpxSlots...),
	}
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
