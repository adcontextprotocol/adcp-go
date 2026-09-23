package identityagent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// identityHandler is the http.Handler for POST /identity. It decodes an
// IdentityMatchRequest under a hard request-timeout budget, runs
// Service.Evaluate, optionally seals a TMPX token, and writes the
// IdentityMatchResponse. Budget overruns produce a 200 response with an
// empty EligiblePackageIDs slice — the same shape callers receive for any
// other fail-closed outcome, so SDKs don't need a special branch.
//
// The handler is wrapped by TMP signature verification at a higher layer
// (see ServeMux assembly in server.go).
type identityHandler struct {
	service                    *Service
	tmpx                       *TMPXSealer
	canonicalizer              *IdentityCanonicalizer
	requestTimeout             time.Duration
	requestBodyLimit           int64
	responseTTL                time.Duration
	supportedADCPMajorVersions map[int]struct{}
	supportedAdcpVersions      map[string]struct{}
	requireConsent             bool
	recorder                   Recorder
	logger                     *slog.Logger
}

// serve_window_sec bounds from the identity-match response schemas. The field
// is required on the provider→router hop, so a sub-second ResponseTTL must not
// truncate to a schema-invalid 0 — the router would then take that 0 as the
// minimum across its fan-out and emit it to the publisher.
//
// router.MinServeWindowSec / MaxServeWindowSec is the same pair in the router
// module; see the comment there for why the two are not shared and what to check
// when the schema bundle moves.
const (
	minServeWindowSec = 1
	maxServeWindowSec = 300
)

// IdentityHandlerConfig packages the inputs for NewIdentityHandler.
type IdentityHandlerConfig struct {
	Service    *Service
	TMPXSealer *TMPXSealer
	// Canonicalizer decodes inbound IdentityToken.UserToken strings into
	// the canonical lowercase-hex key form audience/fcap services lookup
	// on. Construct via NewIdentityCanonicalizer (typically alongside
	// TMPXSealer) so the per-request decode pass runs once and feeds
	// both the shadow request and the TMPX seal step. Nil disables
	// canonicalization — the inbound request is forwarded to
	// service.Evaluate unmodified, preserving the legacy "publisher wire
	// string is the key" behavior.
	Canonicalizer    *IdentityCanonicalizer
	RequestTimeout   time.Duration
	RequestBodyLimit int64
	ResponseTTL      time.Duration
	// SupportedADCPMajorVersions enumerates the AdCP major versions this
	// agent will accept on inbound `adcp_major_version`. When the field is
	// present on a request and not in this set, the handler rejects with
	// HTTP 400 and ErrorCodeInvalidRequest. When the field is omitted, the
	// seller assumes its highest supported version (per the TMP schema).
	SupportedADCPMajorVersions []int

	// SupportedAdcpVersions enumerates the release-precision AdCP versions
	// this agent will accept on inbound `adcp_version` (e.g. "3.0", "3.1",
	// "3.1-beta"). Per version-envelope.json §adcp_version the seller
	// validates the buyer's release pin against this list. When
	// `adcp_version` is set on a request it takes precedence over
	// `adcp_major_version` (deprecated fallback). An empty list disables
	// release-precision validation and the handler falls back to the
	// major-version check only.
	SupportedAdcpVersions []string

	// RequireConsent, when true, rejects any inbound identity-match
	// request that omits the `consent` object with 400 invalid_request.
	// Off by default. Set for buyer deployments operating in
	// jurisdictions where the spec requires consent to accompany user
	// tokens (identity-match-request.json §consent: "Buyers in regulated
	// jurisdictions MUST NOT process the user token without consent
	// information"). This is separate from the schema-level cross-field
	// rule (gdpr:true ⇒ tcf_consent|gpp) that runs regardless in
	// tmproto.ValidateIdentityRequest; RequireConsent adds the
	// presence check the schema cannot express because the jurisdiction
	// is deployment context, not request content.
	RequireConsent bool

	Recorder Recorder
	Logger   *slog.Logger
}

// NewIdentityHandler returns the http.Handler for POST /identity.
// Callers must supply positive RequestTimeout, RequestBodyLimit, and
// ResponseTTL; the agent's Config.Validate enforces this at startup.
func NewIdentityHandler(cfg IdentityHandlerConfig) http.Handler {
	if cfg.Recorder == nil {
		cfg.Recorder = noopRecorder{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	supported := make(map[int]struct{}, len(cfg.SupportedADCPMajorVersions))
	for _, v := range cfg.SupportedADCPMajorVersions {
		supported[v] = struct{}{}
	}
	supportedRel := make(map[string]struct{}, len(cfg.SupportedAdcpVersions))
	for _, v := range cfg.SupportedAdcpVersions {
		supportedRel[v] = struct{}{}
	}
	return &identityHandler{
		service:                    cfg.Service,
		tmpx:                       cfg.TMPXSealer,
		canonicalizer:              cfg.Canonicalizer,
		requestTimeout:             cfg.RequestTimeout,
		requestBodyLimit:           cfg.RequestBodyLimit,
		responseTTL:                cfg.ResponseTTL,
		supportedADCPMajorVersions: supported,
		supportedAdcpVersions:      supportedRel,
		requireConsent:             cfg.RequireConsent,
		recorder:                   cfg.Recorder,
		logger:                     cfg.Logger,
	}
}

func serveWindowSeconds(ttl time.Duration) int {
	seconds := int(ttl.Seconds())
	if seconds > maxServeWindowSec {
		return maxServeWindowSec
	}
	if seconds < minServeWindowSec {
		return minServeWindowSec
	}
	return seconds
}

func (h *identityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h.recorder.RequestStarted(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout)
	defer cancel()

	// MaxBytesReader installs a hard byte cap on the inbound body and
	// surfaces a typed *http.MaxBytesError when exceeded, so we can answer
	// with 413 instead of a generic JSON decode error.
	r.Body = http.MaxBytesReader(w, r.Body, h.requestBodyLimit)

	var req tmproto.IdentityMatchRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			h.writeError(w, "", http.StatusRequestEntityTooLarge, tmproto.ErrorCodeInvalidRequest, "request body too large")
			h.recordCompletion(ctx, start, "body_too_large")
			return
		}
		h.writeError(w, "", http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		h.recordCompletion(ctx, start, "bad_request")
		return
	}
	if err := tmproto.ValidateIdentityRequest(&req); err != nil {
		h.logValidationFailure(r, req.RequestID, err)
		h.writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "invalid request")
		h.recordCompletion(ctx, start, "bad_request")
		return
	}
	// Version negotiation: `adcp_version` (release-precision) is authoritative
	// per version-envelope.json §adcp_version; `adcp_major_version` is a
	// deprecated fallback the seller honors only when `adcp_version` is
	// omitted OR when release-precision validation is not configured on
	// this deployment. Folding the emptiness check into the outer branch
	// condition (rather than into an inner guard) is deliberate: an inner
	// guard would let an `adcp_version`-carrying request slip past both
	// checks entirely when `SupportedAdcpVersions` is empty — the default
	// opt-in state — reintroducing the exact bypass this check exists to
	// close.
	//
	// adcp/schemas/tmp/identity-match-request.json's description names
	// VERSION_UNSUPPORTED here, but the error.json schema's `code` enum
	// does not include it — invalid_request is the closest valid code
	// until the spec is internally consistent.
	if req.AdcpVersion != "" && len(h.supportedAdcpVersions) > 0 {
		if _, ok := h.supportedAdcpVersions[req.AdcpVersion]; !ok {
			h.logValidationFailure(r, req.RequestID, errors.New("adcp_version is not supported"))
			h.writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "invalid request")
			h.recordCompletion(ctx, start, "bad_request")
			return
		}
	} else if req.AdcpMajorVersion != 0 {
		if _, ok := h.supportedADCPMajorVersions[req.AdcpMajorVersion]; !ok {
			h.logValidationFailure(r, req.RequestID, errors.New("adcp_major_version is not supported"))
			h.writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "invalid request")
			h.recordCompletion(ctx, start, "bad_request")
			return
		}
	}

	// Consent-required gate: identity-match-request.json §consent says
	// "Buyers in regulated jurisdictions MUST NOT process the user token
	// without consent information", but the schema cannot express the
	// jurisdiction check (it depends on where the buyer operates, not on
	// the request content). Operators in a jurisdiction that requires
	// consent set CONSENT_REQUIRED=true so the handler rejects a request
	// that omits `consent` before any store lookup runs. The cross-field
	// rule (gdpr:true ⇒ tcf_consent|gpp) still runs unconditionally in
	// tmproto.ValidateIdentityRequest above.
	if h.requireConsent && len(req.Consent) == 0 {
		h.logValidationFailure(r, req.RequestID, errors.New("consent object is required in this jurisdiction"))
		h.writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "invalid request")
		h.recordCompletion(ctx, start, "bad_request")
		return
	}

	serviceReq, decoded := h.buildServiceRequest(ctx, &req)
	var result *targeting.IdentityResult
	if decoded == nil {
		// No canonicalizer wired: nothing to summarize, and the fail-closed
		// policy would over-block on the first request. Preserve the
		// backward-compat path.
		result = h.service.Evaluate(ctx, serviceReq)
	} else {
		summary := DecodeSummary{
			WireCount:    len(req.Identities),
			SuccessCount: decodedSuccessCount(decoded),
		}
		result = h.service.EvaluateWithDecode(ctx, serviceReq, summary)
	}

	// Terminal-error surface: a request that exhausted the handler's
	// budget OR whose service pipeline reported a non-empty Status (store
	// timeout, provider_unavailable) is returned as a TMP ErrorResponse
	// rather than an empty IdentityMatchResponse. The router discriminates
	// on `type: "error"` and its circuit breaker keys off the error code —
	// without this it cannot tell "provider timed out" from "no eligible
	// packages" and healthy providers stay in-rotation regardless of
	// upstream store health. Fail-closed decisions rooted in cap/audience
	// semantics (all-capped, undecodable-identities) keep Status == ""
	// and go through the normal empty-eligibility path below.
	terminalStatus := ""
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		terminalStatus = targeting.StatusTimeout
	} else if result != nil && result.Status != targeting.StatusOK {
		terminalStatus = result.Status
	}
	if terminalStatus != "" {
		errCode := errorCodeForStatus(terminalStatus)
		h.writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), http.StatusOK, errCode, string(errCode))
		h.recordCompletion(ctx, start, terminalStatus)
		return
	}

	eligible := make([]string, 0, len(result.Eligibility))
	for _, e := range result.Eligibility {
		if e.Eligible {
			eligible = append(eligible, e.PackageID)
		}
	}

	resp := &tmproto.ProviderIdentityMatchResponse{
		Type:               tmproto.TypeIdentityMatchResponse,
		RequestID:          result.RequestID,
		EligiblePackageIDs: eligible,
		ServeWindowSec:     serveWindowSeconds(h.responseTTL),
	}
	if h.tmpx != nil && len(eligible) > 0 {
		tmpxStart := time.Now()
		// When a canonicalizer ran, decoded already holds the per-request
		// decode pass and we pass it through to keep the LiveRamp sidecar
		// at one call per request. When canonicalization is opted-out
		// (decoded is nil) the sealer runs its own decode pass over the
		// inbound identities. Verified-identity nullifiers are appended as
		// pre-decoded entries: they come from the verified-identity stage
		// (verify-before-trust) and are never decoded from inbound
		// sender-asserted identities.
		seal := decoded
		if seal == nil {
			seal = h.tmpx.Decode(ctx, req.Identities)
		}
		seal = append(seal, h.tmpx.verifiedIdentityEntries(ctx, result.Verified)...)
		token, terr := h.tmpx.SealDecoded(ctx, seal)
		if terr != nil {
			h.logger.Warn("tmpx generation failed, response will omit tmpx",
				"request_id", req.RequestID, "error", terr)
			h.recorder.StageOutcome(ctx, StageTMPX, OutcomeError)
		} else if token != "" {
			assignTmpxToResponse(resp, h.tmpx, token)
			h.recorder.StageOutcome(ctx, StageTMPX, OutcomePass)
		}
		h.recorder.StageDuration(ctx, StageTMPX, time.Since(tmpxStart))
	}

	status := "ok"
	if !h.writeResponse(w, resp) {
		status = "write_error"
	}
	h.logger.Debug("identity match",
		"request_id", req.RequestID,
		"packages", len(req.PackageIDs),
		"eligible", len(eligible),
		"latency_ms", time.Since(start).Milliseconds())
	h.recordCompletion(ctx, start, status)
}

// assignTmpxToResponse populates TmpxChunks on a
// ProviderIdentityMatchResponse from a freshly sealed wire token. Each
// chunk carries the provider-local slot_id (from the registered
// tmpx_slots) that the value fills; the router places the emitted
// tmpx_chunks under tmpx_providers[provider_id] on the router→publisher
// hop, and the publisher's deployment configuration resolves
// (provider_id, slot_id) to the local ad-server destination.
//
// Extracted from ServeHTTP as a package-private helper so the
// slot-pairing bookkeeping can be unit-tested without spinning up a
// full request lifecycle.
func assignTmpxToResponse(resp *tmproto.ProviderIdentityMatchResponse, sealer *TMPXSealer, token string) {
	resp.TmpxChunks = sealer.ChunkEntries(token)
}

// writeResponse marshals payload to JSON in one shot and writes it. Using
// json.Marshal + w.Write avoids the json.Encoder allocation per request
// that json.NewEncoder(w).Encode would incur on the hot path.
//
// Returns true on success and false when either marshal or write failed —
// the caller stamps this onto the request-completion metric so a write
// failure shows up distinctly from "ok".
func (h *identityHandler) writeResponse(w http.ResponseWriter, resp *tmproto.ProviderIdentityMatchResponse) bool {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(resp)
	if err != nil {
		h.logger.Warn("failed to marshal identity response", "request_id", resp.RequestID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return false
	}
	if _, err := w.Write(body); err != nil {
		h.logger.Warn("failed to write identity response", "request_id", resp.RequestID, "error", err)
		return false
	}
	return true
}

func (h *identityHandler) writeError(w http.ResponseWriter, requestID string, status int, code tmproto.ErrorCode, message string) {
	body, err := json.Marshal(tmproto.ErrorResponse{
		Type:      tmproto.TypeError,
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}

func (h *identityHandler) logValidationFailure(r *http.Request, requestID string, err error) {
	attrs := []any{"method", r.Method, "path", r.URL.Path, "error", err}
	if safeID := tmproto.SafeRequestIDForEcho(requestID); safeID != "" {
		attrs = append(attrs, "request_id", safeID)
	} else if requestID != "" {
		attrs = append(attrs, "request_id_valid", false)
	}
	h.logger.Warn("invalid identity-match request", attrs...)
}

func (h *identityHandler) recordCompletion(ctx context.Context, start time.Time, status string) {
	h.recorder.RequestCompleted(ctx, status, time.Since(start))
}

// buildServiceRequest prepares the IdentityMatchRequest that flows into
// service.Evaluate, along with the decoded identity slice the TMPX seal
// path will consume.
//
// When a canonicalizer is configured the request's identities are decoded
// once (so LiveRamp-backed RampIDs hit the sidecar at most once per
// request) and a shallow shadow request is built whose Identities slice
// carries only the entries that successfully decoded, with UserToken set
// to the canonical lowercase-hex form of the decoded bytes — that way
// identityhash.Hash(user_token) inside audience/fcap keys onto the same
// shape ExposureLog.user_token publishes downstream, which is the
// keying convention downstream marker writers and buyer-master readers
// honor. The same decoded slice flows through to TMPXSealer.SealDecoded
// when TMPX is enabled, so the decode pass runs exactly once per request
// regardless of whether one or both of canonicalization and sealing are
// active.
//
// When no canonicalizer is configured no decode is performed and the
// original request passes through unchanged — preserving legacy behavior
// for deployments that have opted out of canonicalization. (Production
// wires a canonicalizer in by default; this branch is the explicit
// opt-out and the test-fixture default.)
//
// The returned shadow shares backing storage for every IdentityMatchRequest
// field except Identities; the caller must not append to those slices.
// service.Evaluate is the only consumer and does its own value copy of
// the request before mutating PackageIDs.
func (h *identityHandler) buildServiceRequest(ctx context.Context, req *tmproto.IdentityMatchRequest) (*tmproto.IdentityMatchRequest, []DecodedIdentity) {
	if h.canonicalizer == nil {
		return req, nil
	}
	decoded := h.canonicalizer.Decode(ctx, req.Identities)
	shadow := *req
	shadow.Identities = serviceIdentities(req.Identities, decoded)
	return &shadow, decoded
}

// serviceIdentities is the identity slice Evaluate operates on. It combines the
// successfully-decoded identities (canonical lowercase-hex UserToken, for
// audience/fcap keying) with any inbound identity carrying a verified-identity
// attestation that the canonicalizer could not decode.
//
// The second group exists for identities that are intentionally not
// inbound-decodable — notably a World ID nullifier, which must be
// verifier-derived rather than trusted from a raw token, yet carries the
// attestation the verified-identity stage verifies. Decoding drops both the
// World token and every Attestation, so without this the in-band
// verified-identity path is a no-op whenever a canonicalizer is configured.
// Attestation-less and successfully-decoded identities are already represented
// by the canonical set, so only undecoded attestation carriers are appended
// (no double-counting).
// errorCodeForStatus maps a targeting.Status* value onto the
// tmproto.ErrorCode enum on error.json. Unknown statuses fall back to
// internal_error so the handler never emits an unenumerated code.
func errorCodeForStatus(status string) tmproto.ErrorCode {
	switch status {
	case targeting.StatusTimeout:
		return tmproto.ErrorCodeTimeout
	case targeting.StatusProviderUnavailable:
		return tmproto.ErrorCodeProviderUnavailable
	default:
		return tmproto.ErrorCodeInternalError
	}
}

// decodedSuccessCount tallies decoded identities whose canonicalization
// succeeded (Bytes non-empty). Feeds Service.EvaluateWithDecode's
// DecodeSummary so the fcap stage can fail closed when the request's
// identities cannot be verified against cap-state (TMP invariant #2).
func decodedSuccessCount(decoded []DecodedIdentity) int {
	n := 0
	for _, d := range decoded {
		if len(d.Bytes) > 0 {
			n++
		}
	}
	return n
}

func serviceIdentities(inbound []tmproto.IdentityToken, decoded []DecodedIdentity) []tmproto.IdentityToken {
	out := audienceEligibleIdentities(decoded)
	for i := range inbound {
		if inbound[i].Attestation == nil {
			continue
		}
		if i < len(decoded) && len(decoded[i].Bytes) > 0 {
			continue
		}
		out = append(out, tmproto.IdentityToken{
			UIDType:     inbound[i].UIDType,
			UserToken:   inbound[i].UserToken,
			Attestation: inbound[i].Attestation,
		})
	}
	return out
}
