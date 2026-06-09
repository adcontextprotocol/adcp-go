package router

import (
	"slices"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// MatchesContextProvider checks if a context match request should be sent to this provider.
// Assumes the caller has already filtered by status (e.g., via ProviderSet.Active()).
func MatchesContextProvider(req *tmproto.ContextMatchRequest, p *ProviderConfig) bool {
	if !p.ContextMatch {
		return false
	}
	if !matchesProperty(req.PropertyID, req.PropertyRID, string(req.PropertyType), p) {
		return false
	}
	return true
}

// MatchesIdentityProvider checks if an identity match request should be sent to this provider.
// Filters by country and uid_type when configured on the provider.
// Assumes the caller has already filtered by status (e.g., via ProviderSet.Active()).
func MatchesIdentityProvider(req *tmproto.IdentityMatchRequest, p *ProviderConfig) bool {
	if !p.IdentityMatch {
		return false
	}
	// Country filter: skip when request has no country (backward compat —
	// requests that don't know user country fan out to all providers).
	if len(p.Countries) > 0 && req.Country != "" {
		if !slices.Contains(p.Countries, req.Country) {
			return false
		}
	}
	// Provider passes if any identity in the request matches a uid_type the
	// provider can resolve. An empty provider UIDTypes list matches anything.
	if len(p.UIDTypes) > 0 && len(req.Identities) > 0 {
		matched := false
		for _, id := range req.Identities {
			if slices.Contains(p.UIDTypes, string(id.UIDType)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// filterIdentitiesForProvider returns the subset of identities whose uid_type
// the provider declared — minimum-necessary forwarding, so a provider never
// receives identity tokens for types it cannot resolve. An empty provider
// UIDTypes list matches anything, so the full set is returned. The result is
// always a subset of the input: the router never adds, substitutes, or
// transforms identity tokens.
func filterIdentitiesForProvider(ids []tmproto.IdentityToken, p *ProviderConfig) []tmproto.IdentityToken {
	if len(p.UIDTypes) == 0 {
		return ids
	}
	out := make([]tmproto.IdentityToken, 0, len(ids))
	for _, id := range ids {
		if slices.Contains(p.UIDTypes, string(id.UIDType)) {
			out = append(out, id)
		}
	}
	return out
}

// filterSealedCredentialsForProvider returns only the sealed credentials whose
// audience_kid this provider holds a key for. Entries addressed to other
// audiences are never forwarded — sealed credentials are routed to their owner,
// not broadcast — so the result is nil when the provider declares no audience
// keys or none of the entries match.
func filterSealedCredentialsForProvider(creds []tmproto.SealedCredential, p *ProviderConfig) []tmproto.SealedCredential {
	if len(creds) == 0 || len(p.AudienceKIDs) == 0 {
		return nil
	}
	out := make([]tmproto.SealedCredential, 0, len(creds))
	for _, sc := range creds {
		if slices.Contains(p.AudienceKIDs, sc.AudienceKID) {
			out = append(out, sc)
		}
	}
	return out
}

func matchesProperty(propertyID, propertyRID, propertyType string, p *ProviderConfig) bool {
	// Check exclusions first (by slug)
	for _, pattern := range p.ExcludePropertyIDs {
		if matchGlob(pattern, propertyID) {
			return false
		}
	}

	// Check property ID allowlist (by slug)
	if len(p.PropertyIDs) > 0 {
		found := false
		for _, pattern := range p.PropertyIDs {
			if matchGlob(pattern, propertyID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check property RID allowlist (exact UUID match).
	// Populated by discovery from ProviderRegistration.Properties.
	if len(p.PropertyRIDs) > 0 {
		if propertyRID == "" || !slices.Contains(p.PropertyRIDs, propertyRID) {
			return false
		}
	}

	// Check property type allowlist
	if len(p.PropertyTypes) > 0 {
		found := slices.Contains(p.PropertyTypes, propertyType)
		if !found {
			return false
		}
	}

	return true
}

// matchGlob matches a simple glob pattern against a string.
// Supports only '*' (matches any sequence of characters) and '?' (matches any single character).
// Unlike filepath.Match, this has no platform-specific behavior.
// Uses a linear-time NFA algorithm — O(len(pattern) * len(s)) worst case.
func matchGlob(pattern, s string) bool {
	px, sx := 0, 0
	starPx, starSx := -1, -1
	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
		} else if px < len(pattern) && pattern[px] == '*' {
			starPx = px
			starSx = sx
			px++
		} else if starPx >= 0 {
			starSx++
			sx = starSx
			px = starPx + 1
		} else {
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}
