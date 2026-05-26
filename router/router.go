package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// FanOutMetrics is called during fan-out to record provider exclusions.
type FanOutMetrics interface {
	IncExcluded(providerID string)
}

// Router fans out TMP requests to registered providers and merges responses.
type Router struct {
	providers              *ProviderSet
	registry               *Registry
	health                 *ProviderHealth // nil = no health tracking
	latencyBudget          time.Duration
	client                 *http.Client
	logger                 *slog.Logger
	metrics                FanOutMetrics
	skipEndpointValidation bool

	// TMP request signing per spec §"Request Authentication".
	// signer is nil only when the deployer has explicitly opted out of signing
	// (e.g., for local dev). Production deployments MUST set a signer — the
	// spec mandates Ed25519 request authentication on all router→provider
	// fan-outs.
	signer      *tmproto.Signer
	contextSigs *contextSignatureCache
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

// WithLatencyBudget sets the overall fan-out latency budget.
// Per-provider timeouts are clamped to this value.
func WithLatencyBudget(d time.Duration) RouterOption {
	return func(r *Router) { r.latencyBudget = d }
}

// WithFanOutMetrics sets the metrics callback for fan-out exclusion tracking.
func WithFanOutMetrics(m FanOutMetrics) RouterOption {
	return func(r *Router) { r.metrics = m }
}

// WithTMPSigner attaches an Ed25519 signer that the router uses to sign
// every outbound /context and /identity request per the TMP specification.
// Required for any deployment that talks to spec-conformant providers. The
// router holds onto signer for the rest of its lifetime.
func WithTMPSigner(signer *tmproto.Signer) RouterOption {
	return func(r *Router) { r.signer = signer }
}

// Providers returns the router's provider set for use by health checkers and discovery.
func (r *Router) Providers() *ProviderSet { return r.providers }

// NewRouter creates a router with the given provider configuration and registry.
// Returns an error if any provider endpoint fails SSRF validation.
//
// Provider fan-outs are signed per the TMP spec §"Request Authentication"
// (Ed25519 over X-AdCP-Signature / X-AdCP-Key-Id). Pass WithTMPSigner to
// supply the signing key — without it, fan-outs go out unsigned and providers
// configured to require signatures will reject the requests.
func NewRouter(providers []ProviderConfig, registry *Registry, health *ProviderHealth, opts ...RouterOption) (*Router, error) {
	maxPerHost := max(len(providers), 10)
	r := &Router{
		providers:   NewProviderSet(providers),
		registry:    registry,
		health:      health,
		logger:      slog.Default(),
		contextSigs: newContextSignatureCache(0),
	}
	for _, o := range opts {
		o(r)
	}
	// Set default client if not overridden by options.
	// Use safeDialContext in production to prevent DNS rebinding attacks.
	if r.client == nil {
		transport := &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: maxPerHost,
			IdleConnTimeout:     90 * time.Second,
		}
		if !r.skipEndpointValidation {
			transport.DialContext = safeDialContext
		}
		r.client = &http.Client{Transport: transport}
	}
	if !r.skipEndpointValidation {
		for _, p := range r.providers.All() {
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
		r.logValidationFailure("invalid context-match request", req, cmReq.RequestID, err)
		r.writeError(w, tmproto.SafeRequestIDForEcho(cmReq.RequestID), tmproto.ErrorCodeInvalidRequest, "invalid request")
		return
	}

	// Enrich with registry data — resolve property_rid for fast provider-side matching
	if r.registry != nil {
		if prop, ok := r.registry.LookupByID(cmReq.PropertyID); ok {
			cmReq.PropertyRID = prop.PropertyRID
		}
	}

	// Strip per-asset Access credentials before fan-out — the spec says
	// routers MUST drop bearer tokens, service accounts, and credentials
	// because the request is replicated to every matching buyer agent.
	cmReq.Artifact.StripAccess()

	// Re-serialize with enriched and sanitized data for fan-out.
	body, err = json.Marshal(&cmReq)
	if err != nil {
		r.writeError(w, cmReq.RequestID, tmproto.ErrorCodeInternalError, "failed to serialize request")
		return
	}

	// Find matching providers
	var matching []ProviderConfig
	for _, p := range r.providers.Active() {
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
		r.logValidationFailure("invalid identity-match request", req, imReq.RequestID, err)
		r.writeError(w, tmproto.SafeRequestIDForEcho(imReq.RequestID), tmproto.ErrorCodeInvalidRequest, "invalid request")
		return
	}

	// Find matching providers (filtered by country + uid_type).
	var matching []ProviderConfig
	for _, p := range r.providers.Active() {
		if MatchesIdentityProvider(&imReq, &p) {
			matching = append(matching, p)
		}
	}

	// Strip country before forwarding — it's a routing directive, not an
	// identity signal — and not part of the signing input either.
	imReq.Country = ""
	body, err = json.Marshal(&imReq)
	if err != nil {
		r.logger.Error("failed to serialize identity-match request", "request_id", imReq.RequestID, "error", err)
		r.writeError(w, imReq.RequestID, tmproto.ErrorCodeInternalError, "internal error")
		return
	}

	// Fan out — signer needs the parsed request (not just bytes) to build the
	// JCS canonical form per provider.
	results := r.fanOutIdentity(req.Context(), matching, &imReq, body)

	// Merge — extract parallel slices for provider IDs and responses.
	providerIDs := make([]string, len(results))
	responses := make([]*tmproto.IdentityMatchResponse, len(results))
	for i, r := range results {
		providerIDs[i] = r.providerID
		responses[i] = r.response
	}
	merged := mergeIdentityResponses(imReq.RequestID, providerIDs, responses)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		r.logger.Debug("failed to write identity response", "error", err)
	}
}

// effectiveTimeout returns the per-call timeout, clamped to the latency budget.
func (r *Router) effectiveTimeout(providerTimeout time.Duration) time.Duration {
	t := providerTimeout
	if t == 0 {
		t = 30 * time.Millisecond
	}
	if r.latencyBudget > 0 && t > r.latencyBudget {
		t = r.latencyBudget
	}
	return t
}

func (r *Router) fanOutContext(ctx context.Context, providers []ProviderConfig, cmReq *tmproto.ContextMatchRequest, body []byte) []*tmproto.ContextMatchResponse {
	var mu sync.Mutex
	results := make([]*tmproto.ContextMatchResponse, 0, len(providers))
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Go(func() {
			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				if r.metrics != nil {
					r.metrics.IncExcluded(p.ID)
				}
				return
			}
			if r.health != nil {
				r.health.IncrInflight(p.ID)
				defer r.health.DecrInflight(p.ID)
			}

			callCtx, cancel := context.WithTimeout(ctx, r.effectiveTimeout(p.Timeout))
			defer cancel()

			// Filter packages if provider has PackageIDs configured. The
			// signing input must reflect what the provider actually receives,
			// so we sign over the filtered request — not the original.
			signed := cmReq
			callBody := body
			if len(p.PackageIDs) > 0 {
				filtered := *cmReq
				filtered.PackageIDs = filterPackageIDsForProvider(cmReq.PackageIDs, &p)
				signed = &filtered
				var err error
				callBody, err = json.Marshal(&filtered)
				if err != nil {
					r.logger.Error("failed to serialize filtered request", "provider", p.ID, "error", err)
					return
				}
			}

			sigHeaders := r.signContextHeaders(signed, p.Endpoint)

			var cmResp tmproto.ContextMatchResponse
			if err := r.callProvider(callCtx, p.Endpoint+"/context", callBody, sigHeaders, &cmResp); err != nil {
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

type identityResult struct {
	providerID string
	response   *tmproto.IdentityMatchResponse
}

func (r *Router) fanOutIdentity(ctx context.Context, providers []ProviderConfig, imReq *tmproto.IdentityMatchRequest, body []byte) []identityResult {
	var mu sync.Mutex
	var results []identityResult
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Go(func() {
			if r.health != nil && r.health.IsCircuitOpen(p.ID) {
				if r.metrics != nil {
					r.metrics.IncExcluded(p.ID)
				}
				return
			}
			if r.health != nil {
				r.health.IncrInflight(p.ID)
				defer r.health.DecrInflight(p.ID)
			}

			callCtx, cancel := context.WithTimeout(ctx, r.effectiveTimeout(p.Timeout))
			defer cancel()

			sigHeaders, err := r.signIdentityHeaders(imReq, p.Endpoint)
			if err != nil {
				r.logger.Error("failed to sign identity match request", "provider", p.ID, "error", err)
				return
			}

			var imResp tmproto.IdentityMatchResponse
			if err := r.callProvider(callCtx, p.Endpoint+"/identity", body, sigHeaders, &imResp); err != nil {
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
			results = append(results, identityResult{providerID: p.ID, response: &imResp})
			mu.Unlock()
		})
	}

	wg.Wait()
	return results
}

func (r *Router) callProvider(ctx context.Context, endpoint string, body []byte, headers map[string]string, target any) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

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
		Type:      tmproto.TypeContextMatchResponse,
		RequestID: requestID,
		Offers:    []tmproto.Offer{},
	}

	mergedSignals := make(map[string]any)

	for _, resp := range responses {
		merged.Offers = append(merged.Offers, resp.Offers...)
		maps.Copy(mergedSignals, resp.Signals)
	}

	if len(mergedSignals) > 0 {
		merged.Signals = mergedSignals
	}

	return merged
}

// mergeIdentityResponses combines eligibility from multiple providers.
// Packages are provider-specific — duplicates across providers are a config error.
// Merge is union: a package listed by any provider is eligible.
// TTL: minimum across providers. TMPX: collected per provider ID.
func mergeIdentityResponses(requestID string, providerIDs []string, responses []*tmproto.IdentityMatchResponse) *tmproto.IdentityMatchResponse {
	eligibleSet := make(map[string]struct{})
	minTTL := -1
	var tmpx string

	for _, resp := range responses {
		if resp.Tmpx != "" {
			tmpx = resp.Tmpx
		}
		if minTTL < 0 || resp.TTLSec < minTTL {
			minTTL = resp.TTLSec
		}
		for _, pkgID := range resp.EligiblePackageIDs {
			eligibleSet[pkgID] = struct{}{}
		}
	}

	eligible := make([]string, 0, len(eligibleSet))
	for pkgID := range eligibleSet {
		eligible = append(eligible, pkgID)
	}
	sort.Strings(eligible)

	if minTTL < 0 {
		minTTL = 0
	}

	merged := &tmproto.IdentityMatchResponse{
		Type:               tmproto.TypeIdentityMatchResponse,
		RequestID:          requestID,
		EligiblePackageIDs: eligible,
		TTLSec:             minTTL,
		Tmpx:               tmpx,
	}
	return merged
}

// filterPackageIDsForProvider filters a PackageIDs list if the provider has PackageIDs configured.
func filterPackageIDsForProvider(pkgIDs []string, p *ProviderConfig) []string {
	if len(p.PackageIDs) == 0 {
		return pkgIDs
	}
	allowed := make(map[string]bool, len(p.PackageIDs))
	for _, id := range p.PackageIDs {
		allowed[id] = true
	}
	var filtered []string
	for _, id := range pkgIDs {
		if allowed[id] {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

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
		Type:      tmproto.TypeError,
		RequestID: requestID,
		Code:      code,
		Message:   message,
	}); err != nil {
		r.logger.Debug("failed to write error response", "error", err)
	}
}

func (r *Router) logValidationFailure(message string, req *http.Request, requestID string, err error) {
	attrs := []any{"method", req.Method, "path", req.URL.Path, "error", err}
	if safeID := tmproto.SafeRequestIDForEcho(requestID); safeID != "" {
		attrs = append(attrs, "request_id", safeID)
	} else if requestID != "" {
		attrs = append(attrs, "request_id_valid", false)
	}
	r.logger.Warn(message, attrs...)
}
