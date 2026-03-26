package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// Router fans out TMP requests to registered providers and merges responses.
type Router struct {
	providers []ProviderConfig
	registry  *Registry
	sigCache  *SignatureCache // nil = no signing
	client    *http.Client
}

// NewRouter creates a router with the given provider configuration and registry.
// sigCache is optional — pass nil to disable request signing.
func NewRouter(providers []ProviderConfig, registry *Registry, sigCache *SignatureCache) *Router {
	return &Router{
		providers: providers,
		registry:  registry,
		sigCache:  sigCache,
		client: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// HandleContextMatch processes a context match request.
func (r *Router) HandleContextMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		writeError(w, "", tmp.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var cmReq tmp.ContextMatchRequest
	if err := json.Unmarshal(body, &cmReq); err != nil {
		writeError(w, "", tmp.ErrorCodeInvalidRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := ValidateContextRequest(&cmReq); err != nil {
		writeError(w, cmReq.RequestID, tmp.ErrorCodeInvalidRequest, err.Error())
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
		cmReq.URLHash = tmp.HashURL(cmReq.Artifacts[0])
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
	responses := r.fanOutContext(req.Context(), matching, body)

	// Merge responses
	merged := mergeContextResponses(cmReq.RequestID, responses)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(merged)
}

// HandleIdentityMatch processes an identity match request.
func (r *Router) HandleIdentityMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024)) // 64KB max
	if err != nil {
		writeError(w, "", tmp.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var imReq tmp.IdentityMatchRequest
	if err := json.Unmarshal(body, &imReq); err != nil {
		writeError(w, "", tmp.ErrorCodeInvalidRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if err := ValidateIdentityRequest(&imReq); err != nil {
		writeError(w, imReq.RequestID, tmp.ErrorCodeInvalidRequest, err.Error())
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
	json.NewEncoder(w).Encode(merged)
}

func (r *Router) fanOutContext(ctx context.Context, providers []ProviderConfig, body []byte) []*tmp.ContextMatchResponse {
	var mu sync.Mutex
	var results []*tmp.ContextMatchResponse
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Add(1)
		go func(p ProviderConfig) {
			defer wg.Done()

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp, err := r.callProvider(callCtx, p.Endpoint+"/tmp/context", body)
			if err != nil {
				return // Provider excluded from this response
			}

			var cmResp tmp.ContextMatchResponse
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

func (r *Router) fanOutIdentity(ctx context.Context, providers []ProviderConfig, body []byte) []*tmp.IdentityMatchResponse {
	var mu sync.Mutex
	var results []*tmp.IdentityMatchResponse
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Add(1)
		go func(p ProviderConfig) {
			defer wg.Done()

			timeout := p.Timeout
			if timeout == 0 {
				timeout = 30 * time.Millisecond
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			resp, err := r.callProvider(callCtx, p.Endpoint+"/tmp/identity", body)
			if err != nil {
				return
			}

			var imResp tmp.IdentityMatchResponse
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// mergeContextResponses combines offers and signals from multiple providers.
func mergeContextResponses(requestID string, responses []*tmp.ContextMatchResponse) *tmp.ContextMatchResponse {
	merged := &tmp.ContextMatchResponse{
		RequestID: requestID,
		Offers:    []tmp.Offer{},
	}

	var allSegments []string
	var allKVs []tmp.KeyValuePair

	for _, resp := range responses {
		merged.Offers = append(merged.Offers, resp.Offers...)
		if resp.Signals != nil {
			allSegments = append(allSegments, resp.Signals.Segments...)
			allKVs = append(allKVs, resp.Signals.TargetingKVs...)
		}
	}

	if len(allSegments) > 0 || len(allKVs) > 0 {
		merged.Signals = &tmp.Signals{
			Segments:     allSegments,
			TargetingKVs: allKVs,
		}
	}

	return merged
}

// mergeIdentityResponses combines eligibility from multiple providers.
// AND semantics: eligible only if NO provider says ineligible. intent_score = max.
func mergeIdentityResponses(requestID string, responses []*tmp.IdentityMatchResponse) *tmp.IdentityMatchResponse {
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

	var eligibility []tmp.PackageEligibility
	for pkgID, m := range byPkg {
		eligibility = append(eligibility, tmp.PackageEligibility{
			PackageID:   pkgID,
			Eligible:    m.eligible,
			IntentScore: m.intentScore,
		})
	}

	return &tmp.IdentityMatchResponse{
		RequestID:   requestID,
		Eligibility: eligibility,
	}
}

func writeError(w http.ResponseWriter, requestID string, code tmp.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusBadRequest
	switch code {
	case tmp.ErrorCodeRateLimited:
		status = http.StatusTooManyRequests
	case tmp.ErrorCodeTimeout:
		status = http.StatusGatewayTimeout
	case tmp.ErrorCodeInternalError:
		status = http.StatusInternalServerError
	case tmp.ErrorCodeProviderUnavailable:
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(tmp.ErrorResponse{
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
}
