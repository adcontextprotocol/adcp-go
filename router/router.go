package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Router fans out TMP requests to registered providers and merges responses.
type Router struct {
	providers              []ProviderConfig
	registry               *Registry
	sigCache               *SignatureCache // nil = no signing
	health                 *ProviderHealth // nil = no health tracking
	client                 *http.Client
	logger                 *slog.Logger
	skipEndpointValidation bool
}

// RouterOption configures a Router.
type RouterOption func(*Router)

// WithHTTPClient sets the HTTP client used for provider calls.
// Use this to inject custom TLS configuration, tracing middleware,
// or connection pooling when embedding the router in another system.
func WithHTTPClient(c *http.Client) RouterOption {
	return func(r *Router) { r.client = c }
}

// WithLogger sets the logger for the router. Defaults to slog.Default().
func WithLogger(l *slog.Logger) RouterOption {
	return func(r *Router) { r.logger = l }
}

// WithoutEndpointValidation disables SSRF validation of provider endpoints.
// For use in tests only — never use in production.
func WithoutEndpointValidation() RouterOption {
	return func(r *Router) { r.skipEndpointValidation = true }
}

// NewRouter creates a router with the given provider configuration and registry.
// sigCache is optional — pass nil to disable request signing.
// Returns an error if any provider endpoint fails SSRF validation.
func NewRouter(providers []ProviderConfig, registry *Registry, sigCache *SignatureCache, health *ProviderHealth, opts ...RouterOption) (*Router, error) {
	maxPerHost := max(len(providers), 10)
	r := &Router{
		providers: providers,
		registry:  registry,
		sigCache:  sigCache,
		health:    health,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: maxPerHost,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(r)
	}
	if !r.skipEndpointValidation {
		for _, p := range providers {
			if err := ValidateProviderEndpoint(p.Endpoint); err != nil {
				return nil, fmt.Errorf("provider %q: %w", p.ID, err)
			}
		}
	}
	return r, nil
}

// HandleContextMatch processes a context match request.
func (r *Router) HandleContextMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var cmReq tmproto.ContextMatchRequest
	if err := json.Unmarshal(body, &cmReq); err != nil {
		r.logger.Debug("invalid JSON in context match request", "error", err)
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}

	if err := ValidateContextRequest(&cmReq); err != nil {
		r.writeError(w, cmReq.RequestID, tmproto.ErrorCodeInvalidRequest, err.Error())
		return
	}

	// Enrich with registry data — resolve property_rid for fast provider-side matching
	if r.registry != nil {
		if prop, ok := r.registry.LookupByID(cmReq.PropertyID); ok {
			cmReq.PropertyRID = prop.PropertyRID
		}
	}

	// Compute URL hash from first artifact for fast blocklist/allowlist checks
	if len(cmReq.Artifacts) > 0 {
		cmReq.URLHash = tmproto.HashURL(cmReq.Artifacts[0])
	}

	// Sign the request (cached — ~57ns for cache hit vs ~14μs for cold sign)
	if r.sigCache != nil {
		cmReq.Signature = r.sigCache.SignOrCache(&cmReq)
	}

	// Re-serialize with enriched + signed data for fan-out
	body, err = json.Marshal(&cmReq)
	if err != nil {
		r.writeError(w, cmReq.RequestID, tmproto.ErrorCodeInternalError, "failed to serialize request")
		return
	}

	// Find matching providers
	var matching []ProviderConfig
	for _, p := range r.providers {
		if MatchesContextProvider(&cmReq, &p) {
			matching = append(matching, p)
		}
	}

	// Fan out to matching providers in parallel
	responses := r.fanOutContext(req.Context(), matching, &cmReq, body)

	// Merge responses
	merged := mergeContextResponses(cmReq.RequestID, responses)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		r.logger.Debug("failed to write context response", "error", err)
	}
}

// HandleIdentityMatch processes an identity match request.
func (r *Router) HandleIdentityMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var imReq tmproto.IdentityMatchRequest
	if err := json.Unmarshal(body, &imReq); err != nil {
		r.logger.Debug("invalid JSON in identity match request", "error", err)
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}

	if err := ValidateIdentityRequest(&imReq); err != nil {
		r.writeError(w, imReq.RequestID, tmproto.ErrorCodeInvalidRequest, err.Error())
		return
	}

	// Find matching providers
	var matching []ProviderConfig
	for _, p := range r.providers {
		if MatchesIdentityProvider(&p) {
			matching = append(matching, p)
		}
	}

	// Fan out
	responses := r.fanOutIdentity(req.Context(), matching, body)

	// Merge
	merged := mergeIdentityResponses(imReq.RequestID, responses)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		r.logger.Debug("failed to write identity response", "error", err)
	}
}

func (r *Router) fanOutContext(ctx context.Context, providers []ProviderConfig, cmReq *tmproto.ContextMatchRequest, body []byte) []*tmproto.ContextMatchResponse {
	var mu sync.Mutex
	results := make([]*tmproto.ContextMatchResponse, 0, len(providers))
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Go(func() {
			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				return
			}

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// Filter packages if provider has PackageIDs configured.
			callBody := body
			if len(p.PackageIDs) > 0 {
				filtered := *cmReq
				filtered.AvailablePkgs = filterPackagesForProvider(cmReq.AvailablePkgs, &p)
				var err error
				callBody, err = json.Marshal(&filtered)
				if err != nil {
					r.logger.Error("failed to serialize filtered request", "provider", p.ID, "error", err)
					return
				}
			}

			var cmResp tmproto.ContextMatchResponse
			if err := r.callProvider(callCtx, p.Endpoint+"/tmp/context", callBody, &cmResp); err != nil {
				if r.health != nil {
					if callCtx.Err() != nil {
						r.health.RecordTimeout(p.ID)
					} else {
						r.health.RecordFailure(p.ID)
					}
				}
				return
			}
			if r.health != nil {
				r.health.RecordSuccess(p.ID)
			}

			mu.Lock()
			results = append(results, &cmResp)
			mu.Unlock()
		})
	}

	wg.Wait()
	return results
}

func (r *Router) fanOutIdentity(ctx context.Context, providers []ProviderConfig, body []byte) []*tmproto.IdentityMatchResponse {
	var mu sync.Mutex
	results := make([]*tmproto.IdentityMatchResponse, 0, len(providers))
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Go(func() {
			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				return
			}

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			var imResp tmproto.IdentityMatchResponse
			if err := r.callProvider(callCtx, p.Endpoint+"/tmp/identity", body, &imResp); err != nil {
				if r.health != nil {
					if callCtx.Err() != nil {
						r.health.RecordTimeout(p.ID)
					} else {
						r.health.RecordFailure(p.ID)
					}
				}
				return
			}
			if r.health != nil {
				r.health.RecordSuccess(p.ID)
			}

			mu.Lock()
			results = append(results, &imResp)
			mu.Unlock()
		})
	}

	wg.Wait()
	return results
}

func (r *Router) callProvider(ctx context.Context, endpoint string, body []byte, target any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provider returned %d", resp.StatusCode)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(target); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

// mergeContextResponses combines offers and signals from multiple providers.
func mergeContextResponses(requestID string, responses []*tmproto.ContextMatchResponse) *tmproto.ContextMatchResponse {
	merged := &tmproto.ContextMatchResponse{
		RequestID: requestID,
		Offers:    []tmproto.Offer{},
	}

	var allSegments []string
	var allKVs []tmproto.KeyValuePair

	for _, resp := range responses {
		merged.Offers = append(merged.Offers, resp.Offers...)
		if resp.Signals != nil {
			allSegments = append(allSegments, resp.Signals.Segments...)
			allKVs = append(allKVs, resp.Signals.TargetingKVs...)
		}
	}

	if len(allSegments) > 0 || len(allKVs) > 0 {
		merged.Signals = &tmproto.Signals{
			Segments:     allSegments,
			TargetingKVs: allKVs,
		}
	}

	return merged
}

// mergeIdentityResponses combines eligibility from multiple providers.
// AND semantics: eligible only if NO provider says ineligible. intent_score = max.
func mergeIdentityResponses(requestID string, responses []*tmproto.IdentityMatchResponse) *tmproto.IdentityMatchResponse {
	type mergedElig struct {
		eligible    bool
		intentScore *float64
	}
	byPkg := make(map[string]*mergedElig)

	for _, resp := range responses {
		for _, e := range resp.Eligibility {
			m, ok := byPkg[e.PackageID]
			if !ok {
				// First time seeing this package: use provider's value
				m = &mergedElig{eligible: e.Eligible}
				byPkg[e.PackageID] = m
			} else if !e.Eligible {
				// AND: if any provider says ineligible, final is ineligible
				m.eligible = false
			}
			if e.IntentScore != nil {
				if m.intentScore == nil || *e.IntentScore > *m.intentScore {
					score := *e.IntentScore
					m.intentScore = &score
				}
			}
		}
	}

	var eligibility []tmproto.PackageEligibility
	for pkgID, m := range byPkg {
		eligibility = append(eligibility, tmproto.PackageEligibility{
			PackageID:   pkgID,
			Eligible:    m.eligible,
			IntentScore: m.intentScore,
		})
	}

	return &tmproto.IdentityMatchResponse{
		RequestID:   requestID,
		Eligibility: eligibility,
	}
}

// HandleExpose processes an exposure notification and fans out to identity providers.
func (r *Router) HandleExpose(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024))
	if err != nil {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	// Validate the request.
	var expReq tmproto.ExposeRequest
	if err := json.Unmarshal(body, &expReq); err != nil {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}
	if expReq.PackageID == "" {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "package_id is required")
		return
	}
	if expReq.UserToken == "" && len(expReq.Identities) == 0 {
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "user_token or identities is required")
		return
	}

	// Find identity providers.
	var matching []ProviderConfig
	for _, p := range r.providers {
		if MatchesIdentityProvider(&p) {
			matching = append(matching, p)
		}
	}

	// Fan out to all identity providers; last successful response wins.
	// Expose is a notification (fire-and-forget semantics per provider), so all
	// providers are called regardless. We return the last success to the caller.
	var (
		mu       sync.Mutex
		lastResp *tmproto.ExposeResponse
		wg       sync.WaitGroup
	)

	for _, p := range matching {
		wg.Go(func() {
			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				return
			}

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(req.Context(), timeout)
			defer cancel()

			var expResp tmproto.ExposeResponse
			if err := r.callProvider(callCtx, p.Endpoint+"/tmp/expose", body, &expResp); err != nil {
				r.logger.Debug("expose provider error", "provider", p.ID, "error", err)
				if r.health != nil {
					if callCtx.Err() != nil {
						r.health.RecordTimeout(p.ID)
					} else {
						r.health.RecordFailure(p.ID)
					}
				}
				return
			}
			if r.health != nil {
				r.health.RecordSuccess(p.ID)
			}
			resp := expResp
			mu.Lock()
			lastResp = &resp
			mu.Unlock()
		})
	}
	wg.Wait()

	if lastResp == nil {
		r.writeError(w, "", tmproto.ErrorCodeProviderUnavailable, "no identity providers available")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(lastResp); err != nil {
		r.logger.Debug("failed to write expose response", "error", err)
	}
}

// filterPackagesForProvider filters AvailablePackage list if the provider has PackageIDs configured.
func filterPackagesForProvider(pkgs []tmproto.AvailablePackage, p *ProviderConfig) []tmproto.AvailablePackage {
	if len(p.PackageIDs) == 0 {
		return pkgs
	}
	allowed := make(map[string]bool, len(p.PackageIDs))
	for _, id := range p.PackageIDs {
		allowed[id] = true
	}
	var filtered []tmproto.AvailablePackage
	for _, pkg := range pkgs {
		if allowed[pkg.PackageID] {
			filtered = append(filtered, pkg)
		}
	}
	return filtered
}

// filterPackageIDsForProvider filters a PackageIDs list if the provider has PackageIDs configured.

func (r *Router) writeError(w http.ResponseWriter, requestID string, code tmproto.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusBadRequest
	switch code {
	case tmproto.ErrorCodeRateLimited:
		status = http.StatusTooManyRequests
	case tmproto.ErrorCodeTimeout:
		status = http.StatusGatewayTimeout
	case tmproto.ErrorCodeInternalError:
		status = http.StatusInternalServerError
	case tmproto.ErrorCodeProviderUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(tmproto.ErrorResponse{
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}); err != nil {
		r.logger.Debug("failed to write error response", "error", err)
	}
}
