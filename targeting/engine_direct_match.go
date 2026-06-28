package targeting

import (
	"slices"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// directMatchPasses evaluates the direct-match fields on cfg against
// the request. Returns false to reject the package; true means the
// per-field gates either passed or were unset.
//
// The fields are an alternative to expressing buyer-typed value
// targeting through ContextSignals cfgs: when the publisher already
// sends the matched value on the request, a direct equality check
// avoids a tautological signal lookup.
//
// Geo dimensions honor the spec hierarchy resolution rule
// (docs/media-buy/advanced-topics/targeting.mdx, "Cross-level
// resolution"): exclusion at a higher level takes precedence over
// inclusion at a more specific level. The country gate runs first;
// a country exclusion match short-circuits the package before
// region and metro are considered.
//
// All non-geo fields are independent gates with no hierarchy: each
// is evaluated in turn and the first failure rejects the package.
//
// Empty fields impose no constraint, so this returns true for any cfg
// that does not set any direct-match field — preserving the legacy
// behavior of every existing config.
func (e *ContextEngine) directMatchPasses(cfg *PackageContextConfig, req *tmproto.ContextMatchRequest, country string) bool {
	// Geo: country first, then region, then metro. Higher-level
	// exclusion short-circuits the lower-level inclusion check.
	if !matchScalar(country, cfg.Countries, cfg.CountriesExclude) {
		return false
	}
	region, _ := req.Geo["region"].(string)
	if !matchScalar(region, cfg.Regions, cfg.RegionsExclude) {
		return false
	}
	if !matchMetro(req.Geo, cfg.Metros, cfg.MetrosExclude) {
		return false
	}

	// ContextSignals scalars and lists. Each independent.
	cs := req.ContextSignals
	var (
		language        string
		sentiment       string
		keywords        []string
		contentPolicies []string
	)
	if cs != nil {
		language = cs.Language
		sentiment = cs.Sentiment
		keywords = cs.Keywords
		contentPolicies = cs.ContentPolicies
	}
	if !matchScalar(language, cfg.Languages, cfg.LanguagesExclude) {
		return false
	}
	if !matchScalar(sentiment, cfg.Sentiments, cfg.SentimentsExclude) {
		return false
	}
	if !matchSet(keywords, cfg.Keywords, cfg.KeywordsExclude) {
		return false
	}
	if !matchSet(contentPolicies, cfg.ContentPolicies, cfg.ContentPoliciesExclude) {
		return false
	}

	// Content-artifact identifier lists. Each list is matched against
	// ArtifactRefs entries of the corresponding Type.
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeEIDR, cfg.EIDRs, cfg.EIDRsExclude) {
		return false
	}
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeGracenote, cfg.Gracenotes, cfg.GracenotesExclude) {
		return false
	}
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeISRC, cfg.ISRCs, cfg.ISRCsExclude) {
		return false
	}
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeGTIN, cfg.GTINs, cfg.GTINsExclude) {
		return false
	}
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeRSSGUID, cfg.RSSGUIDs, cfg.RSSGUIDsExclude) {
		return false
	}
	if !matchArtifactRefs(req.ArtifactRefs, tmproto.ArtifactRefTypeISBN, cfg.ISBNs, cfg.ISBNsExclude) {
		return false
	}

	return true
}

// matchScalar runs an inclusion/exclusion gate on one scalar value
// from the request. Exclusion is checked first so a value in both lists
// (operator error) deterministically rejects rather than passes.
// Inclusion against an empty request value (the publisher did not send
// the field) is treated as a miss: a buyer that asked for any specific
// value should not match a request that supplied none.
func matchScalar(value string, include, exclude []string) bool {
	if len(exclude) > 0 && value != "" && slices.Contains(exclude, value) {
		return false
	}
	if len(include) > 0 {
		if value == "" || !slices.Contains(include, value) {
			return false
		}
	}
	return true
}

// matchSet runs an inclusion/exclusion gate where both the request
// side and the package side are sets, with intersection semantics:
// inclusion passes when at least one request value is in the include
// list; exclusion rejects when any request value is in the exclude list.
func matchSet(values, include, exclude []string) bool {
	if len(exclude) > 0 {
		for _, v := range values {
			if v == "" {
				continue
			}
			if slices.Contains(exclude, v) {
				return false
			}
		}
	}
	if len(include) > 0 {
		matched := false
		for _, v := range values {
			if v == "" {
				continue
			}
			if slices.Contains(include, v) {
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

// matchArtifactRefs filters refs to entries of refType and runs a set
// match on their values. A package list that targets a specific
// identifier type does not match a request carrying no refs of that
// type — same posture as matchScalar with no value.
func matchArtifactRefs(refs []tmproto.ArtifactRef, refType tmproto.ArtifactRefType, include, exclude []string) bool {
	if len(include) == 0 && len(exclude) == 0 {
		return true
	}
	var values []string
	for _, ref := range refs {
		if ref.Type == refType && ref.Value != "" {
			values = append(values, ref.Value)
		}
	}
	return matchSet(values, include, exclude)
}

// matchMetro evaluates a Metros / MetrosExclude pair against the
// request's geo.metro sub-object. The metro on the request is a single
// {system, value}: a package entry matches when its System equals the
// request's system AND the request's value is in the entry's Values.
func matchMetro(geo map[string]any, include, exclude []MetroTarget) bool {
	if len(include) == 0 && len(exclude) == 0 {
		return true
	}
	system, value := metroSystemValue(geo)
	if len(exclude) > 0 && system != "" && value != "" {
		for _, t := range exclude {
			if t.System == system && slices.Contains(t.Values, value) {
				return false
			}
		}
	}
	if len(include) > 0 {
		if system == "" || value == "" {
			return false
		}
		matched := false
		for _, t := range include {
			if t.System == system && slices.Contains(t.Values, value) {
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

// metroSystemValue pulls (system, value) out of the spec's
// geo.metro sub-object. Mirrors metroFromGeo's accessor pattern but
// returns the parts separately so the direct-match path can use them
// without re-splitting the joined "{system}:{value}" form.
func metroSystemValue(geo map[string]any) (string, string) {
	if len(geo) == 0 {
		return "", ""
	}
	m, ok := geo["metro"].(map[string]any)
	if !ok {
		return "", ""
	}
	system, _ := m["system"].(string)
	value, _ := m["value"].(string)
	return system, value
}
