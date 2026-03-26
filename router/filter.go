package main

import (
	"path/filepath"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// MatchesContextProvider checks if a context match request should be sent to this provider.
func MatchesContextProvider(req *tmp.ContextMatchRequest, p *ProviderConfig) bool {
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
		if matched, _ := filepath.Match(pattern, propertyID); matched {
			return false
		}
	}

	// Check property ID allowlist
	if len(p.PropertyIDs) > 0 {
		found := false
		for _, pattern := range p.PropertyIDs {
			if matched, _ := filepath.Match(pattern, propertyID); matched {
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
		found := false
		for _, t := range p.PropertyTypes {
			if t == propertyType {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}
