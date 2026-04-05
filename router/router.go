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
	providers []ProviderConfig
	registry  *Registry
	sigCache  *SignatureCache // nil = no signing
	health    *ProviderHealth // nil = no health tracking
	client    *http.Client
}

// NewRouter creates a router with the given provider configuration and registry.
// sigCache is optional — pass nil to disable request signing.
func NewRouter(providers []ProviderConfig, registry *Registry, sigCache *SignatureCache, health *ProviderHealth) *Router {
	maxPerHost := max(len(providers), 10)
	return &Router{
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
	}
}

// HandleContextMatch processes a context match request.
func (r *Router) HandleContextMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var cmReq tmproto.ContextMatchRequest
	if err := json.Unmarshal(body, &cmReq); err != nil {
		slog.Debug("invalid JSON in context match request", "error", err)
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}

	if err := ValidateContextRequest(&cmReq); err != nil {
		writeError(w, cmReq.RequestID, tmproto.ErrorCodeInvalidRequest, err.Error())
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
	body, _ = json.Marshal(&cmReq)

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
		slog.Debug("failed to write context response", "error", err)
	}
}

// HandleIdentityMatch processes an identity match request.
func (r *Router) HandleIdentityMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var imReq tmproto.IdentityMatchRequest
	if err := json.Unmarshal(body, &imReq); err != nil {
		slog.Debug("invalid JSON in identity match request", "error", err)
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}

	if err := ValidateIdentityRequest(&imReq); err != nil {
		writeError(w, imReq.RequestID, tmproto.ErrorCodeInvalidRequest, err.Error())
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
		slog.Debug("failed to write identity response", "error", err)
	}
}

func (r *Router) fanOutContext(ctx context.Context, providers []ProviderConfig, cmReq *tmproto.ContextMatchRequest, body []byte) []*tmproto.ContextMatchResponse {
	var mu sync.Mutex
	var results []*tmproto.ContextMatchResponse
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Add(1)
		go func(p ProviderConfig) {
			defer wg.Done()

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
				callBody, _ = json.Marshal(&filtered)
			}

			resp, err := r.callProvider(callCtx, p.Endpoint+"/tmp/context", callBody)
			if err != nil {
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

			var cmResp tmproto.ContextMatchResponse
			if err := json.Unmarshal(resp, &cmResp); err != nil {
				return
			}

			mu.Lock()
			results = append(results, &cmResp)
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return results
}

func (r *Router) fanOutIdentity(ctx context.Context, providers []ProviderConfig, body []byte) []*tmproto.IdentityMatchResponse {
	var mu sync.Mutex
	var results []*tmproto.IdentityMatchResponse
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Add(1)
		go func(p ProviderConfig) {
			defer wg.Done()

			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				return
			}

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp, err := r.callProvider(callCtx, p.Endpoint+"/tmp/identity", body)
			if err != nil {
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

			var imResp tmproto.IdentityMatchResponse
			if err := json.Unmarshal(resp, &imResp); err != nil {
				return
			}

			mu.Lock()
			results = append(results, &imResp)
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return results
}

func (r *Router) callProvider(ctx context.Context, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // 1MB max response
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
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	// Validate the request.
	var expReq tmproto.ExposeRequest
	if err := json.Unmarshal(body, &expReq); err != nil {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}
	if expReq.PackageID == "" {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "package_id is required")
		return
	}
	if expReq.UserToken == "" && len(expReq.Identities) == 0 {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "user_token or identities is required")
		return
	}

	// Find identity providers.
	var matching []ProviderConfig
	for _, p := range r.providers {
		if MatchesIdentityProvider(&p) {
			matching = append(matching, p)
		}
	}

	// Fan out to all identity providers (expose is idempotent).
	var lastResp *tmproto.ExposeResponse
	var lastErr error
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, p := range matching {
		wg.Add(1)
		go func(p ProviderConfig) {
			defer wg.Done()
			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(req.Context(), timeout)
			defer cancel()

			resp, err := r.callProvider(callCtx, p.Endpoint+"/tmp/expose", body)
			if err != nil {
				return
			}
			var expResp tmproto.ExposeResponse
			if err := json.Unmarshal(resp, &expResp); err != nil {
				return
			}
			mu.Lock()
			lastResp = &expResp
			lastErr = nil
			mu.Unlock()
		}(p)
	}
	wg.Wait()

	if lastResp == nil {
		if lastErr != nil {
			writeError(w, "", tmproto.ErrorCodeProviderUnavailable, lastErr.Error())
		} else {
			writeError(w, "", tmproto.ErrorCodeProviderUnavailable, "no identity providers available")
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lastResp)
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
func filterPackageIDsForProvider(ids []string, p *ProviderConfig) []string {
	if len(p.PackageIDs) == 0 {
		return ids
	}
	allowed := make(map[string]bool, len(p.PackageIDs))
	for _, id := range p.PackageIDs {
		allowed[id] = true
	}
	var filtered []string
	for _, id := range ids {
		if allowed[id] {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

func writeError(w http.ResponseWriter, requestID string, code tmproto.ErrorCode, message string) {
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
		slog.Debug("failed to write error response", "error", err)
	}
}
