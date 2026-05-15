package identityagent

import (
	"context"
	"errors"
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
// Parent-context expiry (the handler's 40ms budget) terminates both
// goroutines and forces a fail-closed result.
func (s *Service) Evaluate(ctx context.Context, req *tmproto.IdentityMatchRequest) *targeting.IdentityResult {
	effectivePkgIDs, idConfigs := identityconfig.ResolveRequest(s.configSvc, req.SellerAgentURL, req.PackageIDs)
	if len(effectivePkgIDs) == 0 {
		s.recorder.StageOutcome(ctx, StageResolve, OutcomeFail)
		return &targeting.IdentityResult{RequestID: req.RequestID}
	}
	s.recorder.StageOutcome(ctx, StageResolve, OutcomePass)
	req.PackageIDs = effectivePkgIDs
	resolved := &targeting.ResolvedPackages{IdentityConfigs: idConfigs}

	pkgsWithSegments := s.packagesWithSegmentRules(resolved, effectivePkgIDs)
	audienceNeeded := s.audienceSvc != nil && len(pkgsWithSegments) > 0

	parCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg          sync.WaitGroup
		fcapResult  fcapResult
		audResult   audienceResult
		audCanceled bool
	)

	wg.Go(func() {
		fcapResult = s.runFcapStage(parCtx, req, effectivePkgIDs)
		if fcapResult.allCapped(effectivePkgIDs) {
			cancel()
		}
	})

	if audienceNeeded {
		wg.Go(func() {
			audResult = s.runAudienceStage(parCtx, req, resolved, pkgsWithSegments)
			if audResult.allRejected(pkgsWithSegments) {
				cancel()
			}
		})
	} else {
		audCanceled = true // audience stage did not run
	}

	wg.Wait()

	if !audienceNeeded && len(pkgsWithSegments) > 0 {
		// Audience is unconfigured but some packages declare segment
		// rules. Per the documented contract, those packages are
		// ineligible at request time. Mark them rejected in the
		// audience result so the join below sees a definitive verdict.
		audResult = audienceResult{
			rejected: pkgsWithSegments,
			outcome:  OutcomeFail,
		}
		_ = audCanceled
	}

	eligibility := s.joinResults(effectivePkgIDs, pkgsWithSegments, fcapResult, audResult)
	return &targeting.IdentityResult{
		RequestID:   req.RequestID,
		Eligibility: eligibility,
	}
}

// packagesWithSegmentRules returns the subset of pkgIDs whose IdentityConfig
// declares a non-empty TargetSegments rule. The returned slice is a set
// (no duplicates) and preserves caller order.
func (s *Service) packagesWithSegmentRules(resolved *targeting.ResolvedPackages, pkgIDs []string) map[string]struct{} {
	out := make(map[string]struct{})
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

func (s *Service) runFcapStage(ctx context.Context, req *tmproto.IdentityMatchRequest, pkgIDs []string) fcapResult {
	start := time.Now()
	fcapCtx, cancelFcap := context.WithTimeout(ctx, s.fcapTimeout)
	defer cancelFcap()

	lookups := make([]fcap.CapLookup, 0, len(req.Identities)*len(pkgIDs))
	for _, id := range req.Identities {
		for _, pkgID := range pkgIDs {
			lookups = append(lookups, fcap.CapLookup{
				UserIdentity: id.UserToken,
				Field: fcap.Field{
					SellerAgentURL: req.SellerAgentURL,
					PackageID:      pkgID,
				},
			})
		}
	}

	results, err := s.fcap.IsCappedBatch(fcapCtx, lookups)
	dur := time.Since(start)
	s.recorder.StageDuration(ctx, StageFCap, dur)

	if err != nil {
		outcome := OutcomeError
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = OutcomeTimeout
		}
		s.recorder.StoreError(ctx, StageFCap)
		s.recorder.StageOutcome(ctx, StageFCap, outcome)
		return fcapResult{cappedByPkg: failClosedFcap(pkgIDs), outcome: outcome, duration: dur}
	}

	capped := make(map[string]bool, len(pkgIDs))
	for _, id := range pkgIDs {
		capped[id] = false
	}
	for i, l := range lookups {
		if results[i] {
			capped[l.Field.PackageID] = true
		}
	}

	allCapped := true
	for _, id := range pkgIDs {
		if !capped[id] {
			allCapped = false
			break
		}
	}
	outcome := OutcomePass
	if allCapped {
		outcome = OutcomeFail
	}
	s.recorder.StageOutcome(ctx, StageFCap, outcome)
	return fcapResult{cappedByPkg: capped, outcome: outcome, duration: dur}
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

func (s *Service) runAudienceStage(ctx context.Context, req *tmproto.IdentityMatchRequest, resolved *targeting.ResolvedPackages, pkgsWithSegments map[string]struct{}) audienceResult {
	start := time.Now()
	audCtx, cancelAud := context.WithTimeout(ctx, s.audienceTimeout)
	defer cancelAud()

	result, err := s.engine.EvaluateIdentityResolved(audCtx, resolved, req)
	dur := time.Since(start)
	s.recorder.StageDuration(ctx, StageAudience, dur)

	if err != nil {
		outcome := OutcomeError
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = OutcomeTimeout
		}
		s.recorder.StoreError(ctx, StageAudience)
		s.recorder.StageOutcome(ctx, StageAudience, outcome)
		return audienceResult{rejected: copySet(pkgsWithSegments), outcome: outcome, duration: dur}
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
	if len(rejected) == len(pkgsWithSegments) && len(pkgsWithSegments) > 0 {
		outcome = OutcomeFail
	}
	s.recorder.StageOutcome(ctx, StageAudience, outcome)
	return audienceResult{rejected: rejected, outcome: outcome, duration: dur}
}

func copySet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
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
