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
func MatchesIdentityProvider(p *ProviderConfig) bool {
	return p.IdentityMatch
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
func matchGlob(pattern, s string) bool {
	return matchGlobBounded(pattern, s, 0)
}

func matchGlobBounded(pattern, s string, depth int) bool {
	if depth > 100 {
		return false
	}
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Trim consecutive stars
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			// Try matching rest of pattern at every position
			for i := 0; i <= len(s); i++ {
				if matchGlobBounded(pattern, s[i:], depth+1) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		default:
			if len(s) == 0 || pattern[0] != s[0] {
				return false
			}
			pattern = pattern[1:]
			s = s[1:]
		}
	}
	return len(s) == 0
}
