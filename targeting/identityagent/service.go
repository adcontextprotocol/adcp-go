package identityagent

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Service composes the audience-only IdentityEngine with a frequency-cap
// gate. fcap and audience lookups run in parallel under a single request
// context with per-component sub-timeouts; either side short-circuits the
// other on a determining outcome (fcap caps everything → audience cancels;
// audience rules out every package → fcap cancels). Both fail closed on
// timeout or store error.
type Service struct {
	engine          *targeting.IdentityEngine
	fcap            *fcap.Service
	audienceSvc     *audience.Service
	configSvc       *identityconfig.Service
	fcapTimeout     time.Duration
	audienceTimeout time.Duration
	recorder        Recorder
}

// ServiceConfig packages the dependencies for NewService.
type ServiceConfig struct {
	Engine          *targeting.IdentityEngine
	FCap            *fcap.Service
	Audience        *audience.Service
	ConfigService   *identityconfig.Service
	FCapTimeout     time.Duration
	AudienceTimeout time.Duration
	Recorder        Recorder
}

// NewService validates the supplied dependencies and returns a Service.
// FCap and ConfigService are required; Audience may be nil when the deployment
// has no audience Valkey wired up.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.Engine == nil {
		return nil, errors.New("identityagent: Engine is required")
	}
	if cfg.FCap == nil {
		return nil, errors.New("identityagent: FCap is required")
	}
	if cfg.ConfigService == nil {
		return nil, errors.New("identityagent: ConfigService is required")
	}
	if cfg.FCapTimeout <= 0 {
		return nil, errors.New("identityagent: FCapTimeout must be positive")
	}
	if cfg.AudienceTimeout <= 0 {
		return nil, errors.New("identityagent: AudienceTimeout must be positive")
	}
	rec := cfg.Recorder
	if rec == nil {
		rec = noopRecorder{}
	}
	return &Service{
		engine:          cfg.Engine,
		fcap:            cfg.FCap,
		audienceSvc:     cfg.Audience,
		configSvc:       cfg.ConfigService,
		fcapTimeout:     cfg.FCapTimeout,
		audienceTimeout: cfg.AudienceTimeout,
		recorder:        rec,
	}, nil
}

// Evaluate runs the full identity-match pipeline for one request. The
// returned IdentityResult honors both audience gating and fcap gating; a
// package is eligible only when both stages pass for it.
//
// The caller's IdentityMatchRequest is treated as read-only — Evaluate
// computes the effective package set internally and never mutates req.
//
// Parent-context expiry (the handler's 40ms budget) terminates both
// goroutines and forces a fail-closed result.
func (s *Service) Evaluate(ctx context.Context, req *tmproto.IdentityMatchRequest) *targeting.IdentityResult {
	effectivePkgIDs, idConfigs := identityconfig.ResolveRequest(s.configSvc, req.SellerAgentURL, req.PackageIDs)
	if len(effectivePkgIDs) == 0 {
		s.recorder.StageOutcome(ctx, StageResolve, OutcomeFail)
		return &targeting.IdentityResult{RequestID: req.RequestID}
	}
	s.recorder.StageOutcome(ctx, StageResolve, OutcomePass)
	resolved := &targeting.ResolvedPackages{IdentityConfigs: idConfigs}

	pkgsWithSegments := s.packagesWithSegmentRules(resolved, effectivePkgIDs)
	audienceNeeded := s.audienceSvc != nil && len(pkgsWithSegments) > 0

	parCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg         sync.WaitGroup
		fcapResult fcapResult
		audResult  audienceResult
	)

	wg.Go(func() {
		fcapResult = s.runFcapStage(parCtx, req, effectivePkgIDs)
		if fcapResult.allCapped(effectivePkgIDs) {
			cancel()
		}
	})

	if audienceNeeded {
		wg.Go(func() {
			audResult = s.runAudienceStage(parCtx, req, resolved, effectivePkgIDs, pkgsWithSegments)
			if audResult.allRejected(pkgsWithSegments) {
				cancel()
			}
		})
	}

	wg.Wait()

	if !audienceNeeded && len(pkgsWithSegments) > 0 {
		// Audience is unconfigured but some packages declare segment
		// rules. Per the documented contract those packages are
		// ineligible at request time; mark them rejected so joinResults
		// sees a definitive verdict. Copy the set so audResult owns its
		// rejected map and no other code can mutate it underneath us.
		audResult = audienceResult{
			rejected: maps.Clone(pkgsWithSegments),
			outcome:  OutcomeFail,
		}
	}

	eligibility := s.joinResults(effectivePkgIDs, pkgsWithSegments, fcapResult, audResult)
	return &targeting.IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}
}

// packagesWithSegmentRules returns the set of pkgIDs whose IdentityConfig
// declares a non-empty TargetSegments rule. Used to scope the audience
// stage to packages that actually need it.
func (s *Service) packagesWithSegmentRules(resolved *targeting.ResolvedPackages, pkgIDs []string) map[string]struct{} {
	out := make(map[string]struct{}, len(pkgIDs))
	for _, id := range pkgIDs {
		cfg := resolved.IdentityConfigs[id]
		if cfg == nil {
			continue
		}
		if !cfg.TargetSegments.IsEmpty() {
			out[id] = struct{}{}
		}
	}
	return out
}

// fcapResult captures the outcome of the fcap stage. cappedByPkg[pkgID] is
// true when at least one identity from the request is currently capped on
// (SellerAgentURL, pkgID). On timeout/error every package is treated as
// capped (fail-closed) and outcome reflects the cause.
type fcapResult struct {
	cappedByPkg map[string]bool
	outcome     string
	duration    time.Duration
}

func (r fcapResult) allCapped(pkgIDs []string) bool {
	if len(pkgIDs) == 0 {
		return false
	}
	for _, id := range pkgIDs {
		if !r.cappedByPkg[id] {
			return false
		}
	}
	return true
}

// runFcapStage builds the (SellerAgentURL, packageID) field list once,
// extracts user tokens from req.Identities, then defers to
// fcap.Service.IsCappedAny which pipelines the cross-product internally
// using a pooled scratch buffer. The result is the per-package cap verdict
// across all request identities.
func (s *Service) runFcapStage(ctx context.Context, req *tmproto.IdentityMatchRequest, pkgIDs []string) fcapResult {
	start := time.Now()
	fcapCtx, cancelFcap := context.WithTimeout(ctx, s.fcapTimeout)
	defer cancelFcap()

	fields := make([]fcap.Field, len(pkgIDs))
	for i, pkgID := range pkgIDs {
		fields[i] = fcap.Field{SellerAgentURL: req.SellerAgentURL, PackageID: pkgID}
	}
	identities := make([]string, len(req.Identities))
	for i, id := range req.Identities {
		identities[i] = id.UserToken
	}

	cappedByField, err := s.fcap.IsCappedAny(fcapCtx, identities, fields)
	dur := time.Since(start)
	s.recorder.StageDuration(ctx, StageFCap, dur)

	if err != nil {
		outcome := stageErrorOutcome(err)
		// Cancellation by the sibling stage is the intentional
		// short-circuit; it's not a store error. Only count
		// timeout/error against StoreError so the metric reflects real
		// upstream failures.
		if outcome != OutcomeCanceled {
			s.recorder.StoreError(ctx, StageFCap)
		}
		s.recorder.StageOutcome(ctx, StageFCap, outcome)
		return fcapResult{cappedByPkg: failClosedFcap(pkgIDs), outcome: outcome, duration: dur}
	}

	capped := make(map[string]bool, len(pkgIDs))
	allCapped := true
	for i, pkgID := range pkgIDs {
		if cappedByField[i] {
			capped[pkgID] = true
		} else {
			allCapped = false
		}
	}
	outcome := OutcomePass
	if allCapped && len(pkgIDs) > 0 {
		outcome = OutcomeFail
	}
	s.recorder.StageOutcome(ctx, StageFCap, outcome)
	return fcapResult{cappedByPkg: capped, outcome: outcome, duration: dur}
}

// stageErrorOutcome categorises an error from a parallel stage into the
// metric outcome label. Deadline exceeded means the stage's own sub-timeout
// fired; Canceled means the sibling stage cancelled the shared parent ctx
// after determining a final outcome (e.g. fcap saw every package capped
// and short-circuited audience). Everything else is a real upstream error.
func stageErrorOutcome(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled
	default:
		return OutcomeError
	}
}

func failClosedFcap(pkgIDs []string) map[string]bool {
	out := make(map[string]bool, len(pkgIDs))
	for _, id := range pkgIDs {
		out[id] = true
	}
	return out
}

// audienceResult captures the outcome of the audience stage. rejected
// holds the packages whose TargetSegments rule did not match the user; a
// package not in rejected is considered audience-eligible. On timeout or
// error, every package with a non-empty rule is added to rejected
// (fail-closed) and outcome reflects the cause.
type audienceResult struct {
	rejected map[string]struct{}
	outcome  string
	duration time.Duration
}

func (r audienceResult) allRejected(pkgsWithSegments map[string]struct{}) bool {
	if len(pkgsWithSegments) == 0 {
		return false
	}
	for id := range pkgsWithSegments {
		if _, ok := r.rejected[id]; !ok {
			return false
		}
	}
	return true
}

// runAudienceStage shadow-copies the request with the effective package
// IDs before invoking the engine. The engine reads req.PackageIDs to scope
// segment lookups; the original caller's request is left untouched.
func (s *Service) runAudienceStage(ctx context.Context, req *tmproto.IdentityMatchRequest, resolved *targeting.ResolvedPackages, effectivePkgIDs []string, pkgsWithSegments map[string]struct{}) audienceResult {
	start := time.Now()
	audCtx, cancelAud := context.WithTimeout(ctx, s.audienceTimeout)
	defer cancelAud()

	engineReq := *req
	engineReq.PackageIDs = effectivePkgIDs

	result, err := s.engine.EvaluateIdentityResolved(audCtx, resolved, &engineReq)
	dur := time.Since(start)
	s.recorder.StageDuration(ctx, StageAudience, dur)

	if err != nil {
		outcome := stageErrorOutcome(err)
		if outcome != OutcomeCanceled {
			s.recorder.StoreError(ctx, StageAudience)
		}
		s.recorder.StageOutcome(ctx, StageAudience, outcome)
		return audienceResult{rejected: maps.Clone(pkgsWithSegments), outcome: outcome, duration: dur}
	}

	rejected := make(map[string]struct{})
	for _, e := range result.Eligibility {
		if _, hasRule := pkgsWithSegments[e.PackageID]; !hasRule {
			continue
		}
		if !e.Eligible {
			rejected[e.PackageID] = struct{}{}
		}
	}
	outcome := OutcomePass
	if len(pkgsWithSegments) > 0 && len(rejected) == len(pkgsWithSegments) {
		outcome = OutcomeFail
	}
	s.recorder.StageOutcome(ctx, StageAudience, outcome)
	return audienceResult{rejected: rejected, outcome: outcome, duration: dur}
}

// joinResults computes the final per-package eligibility from the parallel
// stage outputs. A package is eligible only when:
//
//   - fcap did not flag it as capped for any identity, AND
//   - audience did not reject it (or the package has no segment rule, in
//     which case audience is a no-op for it).
func (s *Service) joinResults(pkgIDs []string, pkgsWithSegments map[string]struct{}, fc fcapResult, aud audienceResult) []tmproto.PackageEligibility {
	out := make([]tmproto.PackageEligibility, 0, len(pkgIDs))
	for _, id := range pkgIDs {
		eligible := true
		if fc.cappedByPkg[id] {
			eligible = false
		}
		if eligible {
			if _, hasRule := pkgsWithSegments[id]; hasRule {
				if _, rejected := aud.rejected[id]; rejected {
					eligible = false
				}
			}
		}
		out = append(out, tmproto.PackageEligibility{PackageID: id, Eligible: eligible})
	}
	return out
}
