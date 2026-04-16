package router

import (
	"slices"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// MatchesContextProvider checks if a context match request should be sent to this provider.
func MatchesContextProvider(req *tmproto.ContextMatchRequest, p *ProviderConfig) bool {
	if !p.ContextMatch {
		return false
	}
	if !matchesProperty(req.PropertyID, string(req.PropertyType), p) {
		return false
	}
	return true
}

// MatchesIdentityProvider checks if an identity match request should be sent to this provider.
// Filters by country and uid_type when configured on the provider.
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
	if len(p.UIDTypes) > 0 && req.UIDType != "" {
		if !slices.Contains(p.UIDTypes, string(req.UIDType)) {
			return false
		}
	}
	return true
}

func matchesProperty(propertyID, propertyType string, p *ProviderConfig) bool {
	// Check exclusions first
	for _, pattern := range p.ExcludePropertyIDs {
		if matchGlob(pattern, propertyID) {
			return false
		}
	}

	// Check property ID allowlist
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
