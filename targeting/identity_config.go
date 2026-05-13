package targeting

import (
	"context"
	"encoding/json"
	"fmt"
)

// SegmentRule expresses audience-segment criteria for a package as a single
// AND-of-clauses rule. A user matches the rule when:
//
//   - they belong to every segment listed in AllOf,
//   - they belong to at least one segment listed in AnyOf (vacuously
//     satisfied when AnyOf is empty),
//   - they belong to none of the segments listed in NoneOf.
//
// A nil *SegmentRule on PackageIdentityConfig means no audience gating: every
// user is eligible for the package.
type SegmentRule struct {
	AllOf  []string `json:"all_of,omitempty"`
	AnyOf  []string `json:"any_of,omitempty"`
	NoneOf []string `json:"none_of,omitempty"`
}

// IsEmpty reports whether the rule declares no clauses — i.e., it
// references no segment IDs in AllOf, AnyOf, or NoneOf. An empty rule
// trivially matches every user; callers can short-circuit audience
// lookups when IsEmpty is true. A nil receiver is treated as empty.
func (r *SegmentRule) IsEmpty() bool {
	if r == nil {
		return true
	}
	return len(r.AllOf) == 0 && len(r.AnyOf) == 0 && len(r.NoneOf) == 0
}

// Segments returns the deduplicated union of every segment ID referenced by
// the rule across AllOf, AnyOf, and NoneOf. Returns nil for an empty or nil
// rule. Callers use this to scope audience-membership lookups to segments the
// rule actually mentions.
func (r *SegmentRule) Segments() []string {
	if r == nil {
		return nil
	}
	total := len(r.AllOf) + len(r.AnyOf) + len(r.NoneOf)
	if total == 0 {
		return nil
	}
	seen := make(map[string]struct{}, total)
	out := make([]string, 0, total)
	add := func(segs []string) {
		for _, s := range segs {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	add(r.AllOf)
	add(r.AnyOf)
	add(r.NoneOf)
	return out
}

// Matches reports whether the given user-segment set satisfies the rule.
// A nil rule trivially matches every user.
func (r *SegmentRule) Matches(userSegments map[string]struct{}) bool {
	if r == nil {
		return true
	}
	for _, s := range r.AllOf {
		if _, ok := userSegments[s]; !ok {
			return false
		}
	}
	if len(r.AnyOf) > 0 {
		matchedAny := false
		for _, s := range r.AnyOf {
			if _, ok := userSegments[s]; ok {
				matchedAny = true
				break
			}
		}
		if !matchedAny {
			return false
		}
	}
	for _, s := range r.NoneOf {
		if _, ok := userSegments[s]; ok {
			return false
		}
	}
	return true
}

// PackageIdentityConfig is the identity-side configuration for a package,
// keyed by (seller_agent_url, package_id) in the in-memory identityconfig
// service.
type PackageIdentityConfig struct {
	TargetSegments *SegmentRule `json:"target_segments,omitempty"`
}

// batchLoadPackageContextConfigs loads context configs for multiple packages in one MGet.
func batchLoadPackageContextConfigs(ctx context.Context, store Store, pkgIDs []string) (map[string]*PackageContextConfig, error) {
	if len(pkgIDs) == 0 {
		return nil, nil
	}
	keys := make([]string, len(pkgIDs))
	for i, id := range pkgIDs {
		keys[i] = fmt.Sprintf("config:pkg:%s:context", id)
	}
	values, err := store.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*PackageContextConfig, len(pkgIDs))
	for i, val := range values {
		if val == "" {
			continue
		}
		var cfg PackageContextConfig
		if err := json.Unmarshal([]byte(val), &cfg); err != nil {
			continue
		}
		result[pkgIDs[i]] = &cfg
	}
	return result, nil
}
