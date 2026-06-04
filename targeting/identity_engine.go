package targeting

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// IdentityEngine evaluates identity-match requests. It reads pre-resolved
// package identity configs and consults an audience.Service for segment
// membership. It does not touch the targeting context-side storage —
// identity data and context data live in separate processes (identity
// agent / context agent) so user-token signals never traverse the
// context path.
type IdentityEngine struct {
	audience *audience.Service
	metrics  Metrics
}

// IdentityEngineConfig holds all configuration for creating an IdentityEngine.
type IdentityEngineConfig struct {
	Audience *audience.Service // nil = identity evaluation is segment-blind
	Metrics  Metrics           // nil = noop
}

// NewIdentityEngine creates an identity-match engine.
func NewIdentityEngine(cfg IdentityEngineConfig) *IdentityEngine {
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &IdentityEngine{
		audience: cfg.Audience,
		metrics:  metrics,
	}
}

// IdentityResult holds the output of identity evaluation.
type IdentityResult struct {
	RequestID   string
	Eligibility []tmproto.PackageEligibility
}

// EvaluateIdentityResolved evaluates package eligibility for an identity
// match request using pre-resolved identity configs supplied by the
// identity agent's bundle. Returns one PackageEligibility per requested
// package ID, preserving order.
func (e *IdentityEngine) EvaluateIdentityResolved(ctx context.Context, resolved *ResolvedPackages, req *tmproto.IdentityMatchRequest) (*IdentityResult, error) {
	evalStart := time.Now()
	identities := resolveIdentities(req)

	userSegments := e.resolveUserSegments(ctx, identities, collectTargetSegments(resolved, req.PackageIDs))

	var eligibility []tmproto.PackageEligibility
	for _, pkgID := range req.PackageIDs {
		idCfg := resolved.IdentityConfigs[pkgID]
		eligible := true

		if idCfg != nil && !idCfg.TargetSegments.IsEmpty() {
			if e.audience == nil || !idCfg.TargetSegments.Matches(userSegments) {
				eligible = false
				e.metrics.IdentityEvaluated(ctx, StageAudience, false)
			}
		}

		eligibility = append(eligibility, tmproto.PackageEligibility{PackageID: pkgID, Eligible: eligible})
	}

	e.metrics.Latency(ctx, "identity_eval", time.Since(evalStart))

	return &IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}, nil
}

// resolveUserSegments batch-queries audience membership for the identities
// against the supplied segment set, returning the set of segments the user
// belongs to. Returns nil when there is no audience service, no identities,
// or no target segments to evaluate.
func (e *IdentityEngine) resolveUserSegments(ctx context.Context, identities []UserIdentity, targetSegments []string) map[string]struct{} {
	if e.audience == nil || len(identities) == 0 || len(targetSegments) == 0 {
		return nil
	}
	lookups := make([]audience.MembershipLookup, 0, len(identities)*len(targetSegments))
	for _, uid := range identities {
		for _, seg := range targetSegments {
			lookups = append(lookups, audience.MembershipLookup{
				UserToken:  uid.UserToken,
				AudienceID: seg,
			})
		}
	}
	results, err := e.audience.IsMemberBatch(ctx, lookups)
	if err != nil {
		e.metrics.StoreError(ctx, "load_user_audiences", err)
		results = make([]bool, len(lookups))
	}
	matched := make(map[string]struct{})
	for i, l := range lookups {
		if results[i] {
			matched[l.AudienceID] = struct{}{}
		}
	}
	return matched
}

// collectTargetSegments returns the deduplicated union of every segment ID
// referenced (across AllOf/AnyOf/NoneOf) by any package's TargetSegments rule
// in pkgIDs. Returns nil when no requested package has segment targeting.
func collectTargetSegments(resolved *ResolvedPackages, pkgIDs []string) []string {
	seen := make(map[string]struct{})
	for _, pkgID := range pkgIDs {
		cfg := resolved.IdentityConfigs[pkgID]
		if cfg == nil {
			continue
		}
		for _, seg := range cfg.TargetSegments.Segments() {
			seen[seg] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for seg := range seen {
		out = append(out, seg)
	}
	return out
}
