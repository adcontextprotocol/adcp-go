package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/adcontextprotocol/adcp-go/urlcanon"
)

// canonicalizeSellerForCache normalizes a seller_agent_url for use as
// a cache-key component. Mirrors targeting/engine.go's normalization
// before ActivePackages lookup so the router keys the same offer set
// under the same seller identity. Canonicalization failure falls back
// to the raw string; note the asymmetry with the engine, which
// returns an empty offer set on the same failure. A raw-fallback key
// therefore never collides with a real canonical key in practice
// (canonicalization is deterministic, so a URL that fails at the
// router also fails at any downstream using the same canonicalizer,
// and no live entry is ever populated under a raw-fallback key that
// could later collide).
func canonicalizeSellerForCache(raw string) string {
	if raw == "" {
		return ""
	}
	if canonical, err := urlcanon.Canonicalize(raw); err == nil {
		return canonical
	}
	return raw
}

// contextCountry extracts the ISO alpha-2 country from a Context Match
// request's geo map, mirroring targeting/engine.go's use of
// GeoCountryKey. Returns empty string when absent or wrong type.
func contextCountry(geo map[string]any) string {
	if geo == nil {
		return ""
	}
	country, _ := geo["country"].(string)
	return country
}

// serve_window_sec bounds from identity-match-response.json. The field is
// required on the router→publisher hop, so the merged value MUST land inside
// this range: emitting 0 when the fan-out produced no response — or passing an
// out-of-range provider value straight through — yields a schema-invalid
// response.
//
// Duplicated deliberately: targeting/identityagent/handler.go carries the same
// pair. Both packages depend on tmproto, so tmproto is the obvious shared home —
// but consumers pin a *released* tmproto (v0.1.0 today), so adding the constants
// there means neither module can reference them until tmproto is tagged and both
// go.mod files are bumped. Two copies of a schema bound beat a release-ordering
// dependency on every future change to either side.
//
// Drift risk is real and unguarded: the bound originates in
// identity-match-response.json and nothing checks the Go copies against it, so a
// schema bundle that widens the range leaves both copies silently clamping to the
// old one. Grep for MaxServeWindowSec / maxServeWindowSec when bumping
// adcp/schemas/VERSION.
const (
	MinServeWindowSec = 1
	MaxServeWindowSec = 300
)

// clampServeWindowSec constrains a merged serve window to the schema's range.
// The floor also carries the right semantics for an empty fan-out: the
// publisher re-queries on the next opportunity rather than caching a
// no-eligibility answer.
func clampServeWindowSec(n int) int {
	if n < MinServeWindowSec {
		return MinServeWindowSec
	}
	if n > MaxServeWindowSec {
		return MaxServeWindowSec
	}
	return n
}

// MaxRequestBodyBytes caps an inbound request body the router reads
// before validating + fan-out. Sized to match the verifier's
// identity-match default (tmproto.VerifyOptions BodyLimit); raise here
// AND in the per-agent verifier config if you ever ship larger
// request shapes.
const MaxRequestBodyBytes = 64 * 1024

// FanOutMetrics is called during fan-out to record provider exclusions.
// Out-of-tree implementations that satisfy this interface continue to work;
// the spec-aligned per-provider metrics added in v3.1 live on
// FanOutMetricsExt and are reached through a runtime type assertion so an
// existing FanOutMetrics impl does not need to be updated.
type FanOutMetrics interface {
	IncExcluded(providerID string)
}

// FanOutMetricsExt extends FanOutMetrics with the spec-named per-provider
// series from docs/trusted-match/router-architecture.mdx §Monitoring. An
// implementation that satisfies this interface (in addition to FanOutMetrics)
// will receive the additional callbacks; an impl that satisfies only
// FanOutMetrics still works and is the pre-existing surface.
type FanOutMetricsExt interface {
	// ObserveProviderDuration records the per-provider end-to-end call latency
	// for a successful fan-out leg in milliseconds (tmp_provider_duration_ms).
	ObserveProviderDuration(providerID string, ms float64)
	// IncProviderTimeout records a per-provider call that exceeded its timeout
	// budget (tmp_provider_timeout_total).
	IncProviderTimeout(providerID string)
	// IncProviderError records a non-timeout per-provider call failure
	// (tmp_provider_error_total).
	IncProviderError(providerID string)
	// AddOffers records offers emitted in a Context Match response after the
	// per-provider responses are merged (tmp_offers_total).
	AddOffers(n int)
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

	// contextCache is nil when caching is disabled (dev / test) or when
	// the deployer did not wire it. Per spec §Caching, populated caches
	// key on {property_rid, placement_id, provider_id}.
	contextCache *ContextCache
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

// WithContextCache attaches a per-provider Context Match response cache
// (spec §Caching). Pass nil to disable caching — the router will fan
// out on every request.
func WithContextCache(c *ContextCache) RouterOption {
	return func(r *Router) { r.contextCache = c }
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
			// Spec §Transport mandates JSON over HTTP/2 for provider
			// calls. net/http disables HTTP/2 whenever DialContext is
			// set, so ForceAttemptHTTP2 is required to get ALPN
			// negotiation back on the https:// fan-out. The dialer still
			// performs the SSRF/rebinding check — Transport uses it for
			// the TCP connection and layers TLS on top itself, so SNI
			// and certificate verification still use the hostname.
			// Cleartext http:// endpoints stay HTTP/1.1: net/http has no
			// h2c support, and provider endpoints MUST be HTTPS in
			// production (provider-registration.json §endpoint).
			ForceAttemptHTTP2: true,
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
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxRequestBodyBytes))
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			r.writeErrorStatus(w, "", http.StatusRequestEntityTooLarge, tmproto.ErrorCodeInvalidRequest, "request body too large")
			return
		}
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		return
	}

	var cmReq tmproto.ContextMatchRequest
	if err := json.Unmarshal(body, &cmReq); err != nil {
		r.logger.Debug("invalid JSON in context match request", "error", err)
		r.writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		return
	}

	// Enrich with registry data so the request reaching MatchesContextProvider
	// carries both identifiers regardless of which one the publisher sent.
	// Runs before validation: publishers MAY send property_id alone and let the
	// router resolve the wire-required property_rid; publishers MAY send
	// property_rid alone (spec-canonical) and let the router resolve the slug
	// needed by providers configured with PropertyIDs allowlists. A missing
	// registry entry leaves the empty identifier as-is and validation rejects it.
	if r.registry != nil {
		if cmReq.PropertyID != "" {
			if prop, ok := r.registry.LookupByID(cmReq.PropertyID); ok {
				cmReq.PropertyRID = prop.PropertyRID
			}
		} else if cmReq.PropertyRID != "" {
			if prop, ok := r.registry.LookupByRID(cmReq.PropertyRID); ok {
				cmReq.PropertyID = prop.PropertyID
			}
		}
	}

	if err := ValidateContextRequest(&cmReq); err != nil {
		r.logValidationFailure("invalid context-match request", req, cmReq.RequestID, err)
		r.writeError(w, tmproto.SafeRequestIDForEcho(cmReq.RequestID), tmproto.ErrorCodeInvalidRequest, "invalid request")
		return
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
	merged := mergeContextResponses(cmReq.RequestID, responses, r.logger)
	if ext := r.metricsExt(); ext != nil {
		ext.AddOffers(len(merged.Offers))
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		r.logger.Debug("failed to write context response", "error", err)
	}
}

// HandleIdentityMatch processes an identity match request.
func (r *Router) HandleIdentityMatch(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxRequestBodyBytes))
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			r.writeErrorStatus(w, "", http.StatusRequestEntityTooLarge, tmproto.ErrorCodeInvalidRequest, "request body too large")
			return
		}
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

	// Fan out. Each provider receives a minimum-necessary subset of the request
	// — only the identity tokens whose uid_type it declared and only the sealed
	// credentials addressed to a key it holds — and the signature is computed
	// over exactly that subset, so fanOutIdentity marshals per provider rather
	// than reusing one body.
	results := r.fanOutIdentity(req.Context(), matching, &imReq)

	merged := mergeIdentityResponses(imReq.RequestID, results, r.logger)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(merged); err != nil {
		r.logger.Debug("failed to write identity response", "error", err)
	}
}

// metricsExt returns the extended-metrics interface when r.metrics satisfies
// it, else nil. Call sites guard with a nil check so an FanOutMetrics impl
// that only implements the original (IncExcluded) method continues to work.
func (r *Router) metricsExt() FanOutMetricsExt {
	if r.metrics == nil {
		return nil
	}
	ext, _ := r.metrics.(FanOutMetricsExt)
	return ext
}

// classifyCallFailure splits a callProvider error into (timeout, parentCancelled).
// timeout: the per-provider deadline elapsed — counts against the provider's
// timeout budget. parentCancelled: the request's parent context was cancelled
// (client disconnect, server drain) — not the provider's fault, so neither
// timeout nor error attribution applies. Both false means a transport or
// decode error.
func classifyCallFailure(callCtx context.Context) (timeout, parentCancelled bool) {
	err := callCtx.Err()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return true, false
	case errors.Is(err, context.Canceled):
		return false, true
	default:
		return false, false
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

type contextResult struct {
	providerID string
	response   *tmproto.ContextMatchResponse
}

func (r *Router) fanOutContext(ctx context.Context, providers []ProviderConfig, cmReq *tmproto.ContextMatchRequest, body []byte) []contextResult {
	var mu sync.Mutex
	results := make([]contextResult, 0, len(providers))
	var wg sync.WaitGroup

	for _, p := range providers {
		wg.Go(func() {
			// Cache hit short-circuit — before health check, signing, or
			// dial. Spec §Caching lists {property_rid, placement_id,
			// provider_id} as the recommended key; we extend it with
			// canonicalized seller_agent_url and country because this
			// repo's targeting engine scopes ActivePackages by both,
			// and keying on placement alone would let one seller's
			// cached offers be served to another seller's request
			// during the TTL window (cross-tenant disclosure). The
			// request's package_ids are not part of the key because
			// active packages are placement-scoped (spec: "MUST NOT
			// vary by user"), and country stays constant per viewer's
			// geo which the publisher does not vary per user.
			//
			// The cache check runs before the circuit-breaker gate: a
			// warm response is still useful when the provider went
			// down, and the TTL bounds staleness. Race note: if a
			// provider's circuit trips after a targeting-config
			// change but before it can emit a fresh response with
			// cache_ttl=0 (the spec's disable-caching signal), the
			// router keeps serving the pre-change offers until the
			// entry ages out. Bounded by the entry's TTL — the
			// operator trades a brief post-change stale window for
			// availability during the outage.
			cacheSeller := canonicalizeSellerForCache(cmReq.SellerAgentURL)
			cacheCountry := contextCountry(cmReq.Geo)
			if r.contextCache != nil {
				if cached, ok := r.contextCache.Get(cmReq.PropertyRID, cmReq.PlacementID, p.ID, cacheSeller, cacheCountry); ok {
					// The merger overwrites RequestID from the current
					// request downstream (mergeContextResponses), so we
					// don't touch cached.RequestID here — any assignment
					// would be dead.
					mu.Lock()
					results = append(results, contextResult{providerID: p.ID, response: cached})
					mu.Unlock()
					return
				}
			}

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
					if ext := r.metricsExt(); ext != nil {
						ext.IncProviderError(p.ID)
					}
					return
				}
			}

			sigHeaders := r.signContextHeaders(signed, p.Endpoint)

			callStart := time.Now()
			var cmResp tmproto.ContextMatchResponse
			err := r.callProvider(callCtx, p.Endpoint+"/context", callBody, sigHeaders, &cmResp)
			elapsed := time.Since(callStart)
			timeout, parentCancelled := classifyCallFailure(callCtx)
			// Observe duration on every terminal outcome except parent
			// cancellation (router-driven, not provider-attributable). Mirrors
			// router/healthcheck.go: timing every outcome keeps p95/p99 honest;
			// recording only successes silently truncates the slow-failure tail.
			if ext := r.metricsExt(); ext != nil && !parentCancelled {
				ext.ObserveProviderDuration(p.ID, float64(elapsed.Milliseconds()))
			}
			if err != nil {
				if !parentCancelled {
					r.logProviderCallFailure(p.ID, cmReq.RequestID, err)
				}
				if r.health != nil {
					switch {
					case timeout:
						r.health.RecordTimeout(p.ID)
					case !parentCancelled:
						r.health.RecordFailure(p.ID)
					}
				}
				if ext := r.metricsExt(); ext != nil && !parentCancelled {
					if timeout {
						ext.IncProviderTimeout(p.ID)
					} else {
						ext.IncProviderError(p.ID)
					}
				}
				return
			}
			if r.health != nil {
				r.health.RecordSuccess(p.ID)
			}

			// Cache the fresh response BEFORE returning it to the merger.
			// The cache clones on both Put and Get, so downstream mutation
			// (e.g. RequestID overwrites on hits) cannot corrupt entries.
			if r.contextCache != nil {
				r.contextCache.Put(cmReq.PropertyRID, cmReq.PlacementID, p.ID, cacheSeller, cacheCountry, &cmResp)
			}

			mu.Lock()
			results = append(results, contextResult{providerID: p.ID, response: &cmResp})
			mu.Unlock()
		})
	}

	wg.Wait()
	return results
}

type identityResult struct {
	providerID      string
	registeredSlots []string
	response        *tmproto.ProviderIdentityMatchResponse
}

func (r *Router) fanOutIdentity(ctx context.Context, providers []ProviderConfig, imReq *tmproto.IdentityMatchRequest) []identityResult {
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

			// Minimum-necessary forwarding: keep only the identity tokens this
			// provider can resolve and the sealed credentials it can open, then
			// sign and marshal over exactly that subset.
			forward := *imReq
			forward.Identities = filterIdentitiesForProvider(imReq.Identities, &p)
			if len(forward.Identities) == 0 {
				// Nothing this provider can resolve. Skip silently — surfacing
				// skip-vs-forward as telemetry would leak which identity types
				// the user had available.
				return
			}
			forward.SealedCredentials = filterSealedCredentialsForProvider(imReq.SealedCredentials, &p)

			sigHeaders, err := r.signIdentityHeaders(&forward, p.Endpoint)
			if err != nil {
				r.logger.Error("failed to sign identity match request", "provider", p.ID, "error", err)
				if ext := r.metricsExt(); ext != nil {
					ext.IncProviderError(p.ID)
				}
				return
			}
			callBody, err := json.Marshal(&forward)
			if err != nil {
				r.logger.Error("failed to serialize identity-match request", "provider", p.ID, "error", err)
				if ext := r.metricsExt(); ext != nil {
					ext.IncProviderError(p.ID)
				}
				return
			}

			callStart := time.Now()
			var imResp tmproto.ProviderIdentityMatchResponse
			err = r.callProviderStrict(callCtx, p.Endpoint+"/identity", callBody, sigHeaders, &imResp, providerHopForbiddenFields)
			elapsed := time.Since(callStart)
			timeout, parentCancelled := classifyCallFailure(callCtx)
			// Observe duration on every terminal outcome except parent
			// cancellation — see the matching note in fanOutContext.
			if ext := r.metricsExt(); ext != nil && !parentCancelled {
				ext.ObserveProviderDuration(p.ID, float64(elapsed.Milliseconds()))
			}
			if err != nil {
				if !parentCancelled {
					r.logProviderCallFailure(p.ID, imReq.RequestID, err)
				}
				if r.health != nil {
					switch {
					case timeout:
						r.health.RecordTimeout(p.ID)
					case !parentCancelled:
						r.health.RecordFailure(p.ID)
					}
				}
				if ext := r.metricsExt(); ext != nil && !parentCancelled {
					if timeout {
						ext.IncProviderTimeout(p.ID)
					} else {
						ext.IncProviderError(p.ID)
					}
				}
				return
			}
			if r.health != nil {
				r.health.RecordSuccess(p.ID)
			}

			mu.Lock()
			results = append(results, identityResult{
				providerID:      p.ID,
				registeredSlots: p.TmpxSlots,
				response:        &imResp,
			})
			mu.Unlock()
		})
	}

	wg.Wait()
	return results
}

func (r *Router) callProvider(ctx context.Context, endpoint string, body []byte, headers map[string]string, target any) error {
	return r.callProviderStrict(ctx, endpoint, body, headers, target, nil)
}

// providerAppError reports that a provider answered HTTP 200 with a TMP error
// envelope (`{"type": "error", ...}`). Spec §HTTP Status Codes makes this the
// normal shape for an application-level failure, and §Error Response says the
// router SHOULD exclude such providers from the merged response — so this is
// surfaced as a call failure rather than decoded as a successful empty result.
type providerAppError struct {
	Code    tmproto.ErrorCode
	Message string
}

func (e *providerAppError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("provider returned TMP error %q", e.Code)
	}
	return fmt.Sprintf("provider returned TMP error %q: %s", e.Code, e.Message)
}

// logProviderCallFailure records why a fan-out leg produced no result. A TMP
// error envelope is logged at WARN with the provider's own code — that is an
// actionable provider-side fault the operator needs to see — while transport
// and decode failures stay at DEBUG because the per-provider error counter is
// the primary signal for those.
func (r *Router) logProviderCallFailure(providerID, requestID string, err error) {
	var appErr *providerAppError
	if errors.As(err, &appErr) {
		// Only the code, which is a bounded enum. The provider's free-text
		// message is untrusted input and logging it would need sanitizing that
		// this change has no reason to introduce.
		r.logger.Warn("provider returned TMP error — excluding from merged response",
			"provider", providerID,
			"request_id", requestID,
			"code", appErr.Code,
		)
		return
	}
	r.logger.Debug("provider call failed", "provider", providerID, "request_id", requestID, "error", err)
}

// responseMessageTypes reads the `type` discriminator off an already-decoded
// provider response and reports the type that message shape must carry.
// Implemented as a type switch rather than a second JSON pass so the check
// costs nothing on the fan-out hot path. An unrecognized target yields two
// empty strings and the caller skips the check.
func responseMessageTypes(target any) (got, expected string) {
	switch t := target.(type) {
	case *tmproto.ContextMatchResponse:
		return t.Type, tmproto.TypeContextMatchResponse
	case *tmproto.ProviderIdentityMatchResponse:
		return t.Type, tmproto.TypeIdentityMatchResponse
	default:
		return "", ""
	}
}

// checkResponseType rejects a provider response whose `type` discriminator
// declares something other than the message type the target shape expects. A
// TMP error envelope is reported as *providerAppError carrying the provider's
// code and message so the fan-out can log the real reason.
//
// An ABSENT `type` is tolerated: the field is schema-required on responses,
// but the spec places no MUST on the router to police it, and a lenient
// provider that omits it still returns a well-formed body. What must not
// happen — and what this closes — is an error envelope being merged as a
// successful empty result, and the error envelope always carries
// `type: "error"`.
func checkResponseType(target any, respBody []byte) error {
	got, expected := responseMessageTypes(target)
	if got == "" || got == expected {
		return nil
	}
	if got == tmproto.TypeError {
		var errResp tmproto.ErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			return &providerAppError{Code: errResp.Code, Message: errResp.Message}
		}
		return &providerAppError{}
	}
	return fmt.Errorf("provider response has type %q, expected %q", got, expected)
}

// callProviderStrict issues the provider call and rejects the response
// when any top-level JSON key in forbiddenTopLevelKeys is present. Callers
// use this to enforce hop-scoped `not: {anyOf: [{required: [<key>]}, ...]}`
// clauses from the schema — the identity-match path uses it to reject
// router-hop fields (`tmpx_providers`, `tmpx`, ...) and envelope-extension
// fields (`context`, `ext`) that MUST NOT appear on the provider hop per
// provider-identity-match-response.json.
func (r *Router) callProviderStrict(ctx context.Context, endpoint string, body []byte, headers map[string]string, target any, forbiddenTopLevelKeys []string) error {
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

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("read provider response: %w", err)
	}

	if len(forbiddenTopLevelKeys) > 0 {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(respBody, &probe); err != nil {
			return fmt.Errorf("decode provider response: %w", err)
		}
		for _, key := range forbiddenTopLevelKeys {
			if _, present := probe[key]; present {
				return fmt.Errorf("provider response carries forbidden top-level key %q for this hop", key)
			}
		}
	}

	if err := json.Unmarshal(respBody, target); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return checkResponseType(target, respBody)
}

// providerHopForbiddenFields lists the top-level JSON keys that MUST NOT
// appear on a provider→router identity-match response — router-hop
// carriers (tmpx_providers, tmpx), pre-3.1.9 legacy carriers
// (tmpx_values, tmpx_macros), and envelope-extension fields (context,
// ext) that would leak across the identity privacy boundary. Mirrors the
// `not` clause in provider-identity-match-response.json.
var providerHopForbiddenFields = []string{
	"tmpx_providers",
	"tmpx",
	"tmpx_values",
	"tmpx_macros",
	"context",
	"ext",
}

// mergeContextResponses combines offers and signals from multiple providers.
//
// Packages are provider-specific per docs/trusted-match/router-architecture.mdx
// §"Response Aggregation": duplicate `package_id` across providers is a
// configuration error. The router keeps the first response received for a
// duplicated package and SHOULD log a warning, so we dedup by package_id and
// emit a warning naming both providers when the same package_id appears in
// more than one response.
//
// Enrichment signals are concatenated per the same section — see signalsMerger
// for the per-key behavior.
func mergeContextResponses(requestID string, responses []contextResult, logger *slog.Logger) *tmproto.ContextMatchResponse {
	merged := &tmproto.ContextMatchResponse{
		Type:      tmproto.TypeContextMatchResponse,
		RequestID: requestID,
		Offers:    []tmproto.Offer{},
	}

	signals := newSignalsMerger()
	seenPkg := make(map[string]string) // package_id -> first provider that returned it

	for _, res := range responses {
		if res.response == nil {
			continue
		}
		for _, offer := range res.response.Offers {
			if first, dup := seenPkg[offer.PackageID]; dup {
				if logger != nil {
					if first == res.providerID {
						logger.Warn("repeated package_id within a single provider's response — keeping first offer",
							"request_id", requestID,
							"package_id", offer.PackageID,
							"provider", res.providerID,
						)
					} else {
						logger.Warn("duplicate package_id across providers — keeping first response (configuration error)",
							"request_id", requestID,
							"package_id", offer.PackageID,
							"first_provider", first,
							"duplicate_provider", res.providerID,
						)
					}
				}
				continue
			}
			seenPkg[offer.PackageID] = res.providerID
			merged.Offers = append(merged.Offers, offer)
		}
		signals.add(res.providerID, res.response.Signals, requestID, logger)
	}

	merged.Signals = signals.result()

	return merged
}

// mergeIdentityResponses combines eligibility from multiple providers.
//
// Packages are provider-specific per docs/trusted-match/router-architecture.mdx
// §"Response Aggregation": duplicate `package_id` across providers is a
// configuration error. The router uses the minimum `serve_window_sec` across
// providers and SHOULD log a warning. Because providers omit ineligibility
// rather than declaring it ("yes-or-silent"), a duplicate manifests as the
// same `package_id` in multiple providers' eligible lists — we log a warning
// naming all reporting providers and emit the union (the spec's "must be in
// both" rule collapses to union when only yes-responses are observable).
//
// The merged `serve_window_sec` is clamped to the schema's [1, 300] range —
// see clampServeWindowSec. This covers the empty fan-out (no provider
// responded, so there is no minimum to take) and a provider that reports a
// value outside the range.
//
// TMPX collection per the spec §"TMPX collection":
//   - Each agent's TmpxChunks[] is folded into tmpx_providers[provider_id]
//     so per-provider attribution survives the fan-out. Each chunk carries
//     the provider-local slot_id from the agent's registered tmpx_slots;
//     the publisher's deployment configuration
//     (publisher-tmpx-config.json) resolves (provider_id, slot_id) to the
//     local ad-server destination.
//   - Slot-contract enforcement (MUST from adcontextprotocol/adcp#5971):
//     before folding, each provider's emitted slot_id sequence is
//     validated against its registered tmpx_slots via
//     enforceProviderSlotContract. A provider whose sequence is empty,
//     unregistered, reordered, sparse, duplicate, or longer than the
//     registered list has its chunks dropped atomically — the other
//     providers on the same response are unaffected. A warning is
//     logged so a misconfigured provider is observable.
//   - The legacy singular `tmpx` field stays populated on the merged
//     response for back-compat with consumers that haven't moved to
//     tmpx_providers (deprecated, removed in 4.0). Source: the first
//     agent's single-chunk value when it emitted exactly one chunk.
//     Multi-chunk emissions never populate this field because the single
//     string cannot represent multiple chunks.
//   - The router MUST NOT carry tmpx_chunks[] at the root of the outbound
//     response — that field is the agent → router carrier only; leaking it
//     alongside tmpx_providers would give the publisher no schema signal
//     for which to read.
func mergeIdentityResponses(requestID string, results []identityResult, logger *slog.Logger) *tmproto.IdentityMatchResponse {
	eligibleSet := make(map[string]struct{})
	pkgProviders := make(map[string][]string)           // package_id -> distinct provider IDs that listed it
	providerRepeats := make(map[string]map[string]bool) // package_id -> set of providers that repeated it within their own response
	minServeWindowSec := -1
	var legacyTmpx string
	tmpxProviders := make(map[string]tmproto.TmpxProviderEntry)

	for _, res := range results {
		resp := res.response
		if resp == nil {
			continue
		}
		if minServeWindowSec < 0 || resp.ServeWindowSec < minServeWindowSec {
			minServeWindowSec = resp.ServeWindowSec
		}
		providerID := res.providerID
		// TMPX: fold the provider's emitted chunks into the per-provider
		// map. Skip empty providerID entries (defensive: a fan-out result
		// without a provider_id would otherwise stash chunks under "",
		// which any consumer would treat as garbage).
		if len(resp.TmpxChunks) > 0 && providerID != "" {
			if !enforceProviderSlotContract(res.registeredSlots, resp.TmpxChunks) {
				// Contract broken: drop this provider's chunks atomically
				// (do NOT partially trim). Other providers on the same
				// response are unaffected. Surface the misconfig so
				// operators can observe it — the log carries both the
				// registered and emitted slot sequences so the drop
				// reason is diagnosable from the log line alone.
				if logger != nil {
					emitted := make([]string, len(resp.TmpxChunks))
					for i, c := range resp.TmpxChunks {
						emitted[i] = c.SlotID
					}
					logger.Warn("dropping provider's tmpx_chunks: sequence is not an ordered prefix of registered tmpx_slots",
						"request_id", requestID,
						"provider", providerID,
						"registered_slots", res.registeredSlots,
						"emitted_slots", emitted,
					)
				}
			} else {
				tmpxProviders[providerID] = tmproto.TmpxProviderEntry{
					Chunks: append([]tmproto.TmpxChunk(nil), resp.TmpxChunks...),
				}
				// The deprecated single-string `tmpx` carrier can only
				// represent tokens that fit in one macro slot. Multi-chunk
				// responses (>1 TmpxChunks entry) produce a wire that
				// spans multiple slots — any single chunk on its own is a
				// broken ciphertext half that fails AEAD open silently.
				// Populate the legacy field only when the emitting agent
				// produced exactly one chunk; a multi-chunk emission
				// leaves it empty so consumers still reading the
				// deprecated field skip it rather than get identity loss.
				if legacyTmpx == "" && len(resp.TmpxChunks) == 1 {
					legacyTmpx = resp.TmpxChunks[0].Value
				}
			}
		}
		// Track DISTINCT providers per package_id and remember if any single
		// provider repeats a package_id within its own response — eligible
		// arrives raw off the wire with no dedup, so a within-provider repeat
		// is reachable and must not be classified as a cross-provider config
		// error (mirrors the Context-path split between the two warnings).
		seenForPkg := make(map[string]bool)
		for _, pkgID := range resp.EligiblePackageIDs {
			eligibleSet[pkgID] = struct{}{}
			if seenForPkg[pkgID] {
				if !providerRepeats[pkgID][providerID] {
					if providerRepeats[pkgID] == nil {
						providerRepeats[pkgID] = make(map[string]bool)
					}
					providerRepeats[pkgID][providerID] = true
				}
				continue
			}
			seenForPkg[pkgID] = true
			pkgProviders[pkgID] = append(pkgProviders[pkgID], providerID)
		}
	}

	if logger != nil {
		// Sort the emission so test assertions on log order are stable —
		// map iteration is non-deterministic in Go and a fan-out with two
		// duplicates would otherwise log in arbitrary order.
		dupKeys := make([]string, 0, len(pkgProviders))
		for pkgID, provs := range pkgProviders {
			if len(provs) > 1 {
				dupKeys = append(dupKeys, pkgID)
			}
		}
		sort.Strings(dupKeys)
		for _, pkgID := range dupKeys {
			logger.Warn("duplicate package_id across providers' identity-match responses (configuration error)",
				"request_id", requestID,
				"package_id", pkgID,
				"providers", pkgProviders[pkgID],
			)
		}
		repeatKeys := make([]string, 0, len(providerRepeats))
		for pkgID := range providerRepeats {
			repeatKeys = append(repeatKeys, pkgID)
		}
		sort.Strings(repeatKeys)
		for _, pkgID := range repeatKeys {
			provs := make([]string, 0, len(providerRepeats[pkgID]))
			for p := range providerRepeats[pkgID] {
				provs = append(provs, p)
			}
			sort.Strings(provs)
			for _, p := range provs {
				logger.Warn("repeated package_id within a single provider's identity-match response",
					"request_id", requestID,
					"package_id", pkgID,
					"provider", p,
				)
			}
		}
	}

	eligible := make([]string, 0, len(eligibleSet))
	for pkgID := range eligibleSet {
		eligible = append(eligible, pkgID)
	}
	sort.Strings(eligible)

	merged := &tmproto.IdentityMatchResponse{
		Type:               tmproto.TypeIdentityMatchResponse,
		RequestID:          requestID,
		EligiblePackageIDs: eligible,
		ServeWindowSec:     clampServeWindowSec(minServeWindowSec),
		Tmpx:               legacyTmpx,
	}
	if len(tmpxProviders) > 0 {
		merged.TmpxProviders = tmpxProviders
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

// writeErrorStatus writes an error response with an explicit HTTP
// status, bypassing writeError's code → status mapping. Used for body
// admission errors (413 Request Entity Too Large) where the status is
// driven by the wire-level fault rather than a TMP error-code enum.
func (r *Router) writeErrorStatus(w http.ResponseWriter, requestID string, status int, code tmproto.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
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
