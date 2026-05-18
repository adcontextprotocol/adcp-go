package identityagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

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
	requestTimeout             time.Duration
	requestBodyLimit           int64
	responseTTL                time.Duration
	supportedADCPMajorVersions map[int]struct{}
	recorder                   Recorder
	logger                     *slog.Logger
}

// IdentityHandlerConfig packages the inputs for NewIdentityHandler.
type IdentityHandlerConfig struct {
	Service          *Service
	TMPXSealer       *TMPXSealer
	RequestTimeout   time.Duration
	RequestBodyLimit int64
	ResponseTTL      time.Duration
	// SupportedADCPMajorVersions enumerates the AdCP major versions this
	// agent will accept on inbound `adcp_major_version`. When the field is
	// present on a request and not in this set, the handler rejects with
	// HTTP 400 and ErrorCodeInvalidRequest. When the field is omitted, the
	// seller assumes its highest supported version (per the TMP schema).
	SupportedADCPMajorVersions []int
	Recorder                   Recorder
	Logger                     *slog.Logger
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
	return &identityHandler{
		service:                    cfg.Service,
		tmpx:                       cfg.TMPXSealer,
		requestTimeout:             cfg.RequestTimeout,
		requestBodyLimit:           cfg.RequestBodyLimit,
		responseTTL:                cfg.ResponseTTL,
		supportedADCPMajorVersions: supported,
		recorder:                   cfg.Recorder,
		logger:                     cfg.Logger,
	}
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
		h.writeError(w, req.RequestID, http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, err.Error())
		h.recordCompletion(ctx, start, "bad_request")
		return
	}
	if req.AdcpMajorVersion != 0 {
		if _, ok := h.supportedADCPMajorVersions[req.AdcpMajorVersion]; !ok {
			// adcp/schemas/tmp/identity-match-request.json's description
			// names VERSION_UNSUPPORTED here, but the error.json schema's
			// `code` enum does not include it. Use invalid_request — the
			// closest valid code — until the spec is internally
			// consistent. The message preserves diagnostic detail.
			h.writeError(w, req.RequestID, http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest,
				fmt.Sprintf("adcp_major_version %d is not supported", req.AdcpMajorVersion))
			h.recordCompletion(ctx, start, "bad_request")
			return
		}
	}

	result := h.service.Evaluate(ctx, &req)

	// Fail closed on budget overrun: return the standard wire shape with an
	// empty eligible-packages array, matching what callers see for any other
	// fail-closed outcome. RequestID is preserved so the buyer can correlate.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		status := "timeout"
		if !h.writeResponse(w, &tmproto.IdentityMatchResponse{
			Type:               tmproto.TypeIdentityMatchResponse,
			RequestID:          req.RequestID,
			EligiblePackageIDs: []string{},
			TTLSec:             int(h.responseTTL.Seconds()),
		}) {
			status = "write_error"
		}
		h.recordCompletion(ctx, start, status)
		return
	}

	eligible := make([]string, 0, len(result.Eligibility))
	for _, e := range result.Eligibility {
		if e.Eligible {
			eligible = append(eligible, e.PackageID)
		}
	}

	resp := &tmproto.IdentityMatchResponse{
		Type:               tmproto.TypeIdentityMatchResponse,
		RequestID:          result.RequestID,
		EligiblePackageIDs: eligible,
		TTLSec:             int(h.responseTTL.Seconds()),
	}
	if h.tmpx != nil && len(eligible) > 0 {
		tmpxStart := time.Now()
		if token, terr := h.tmpx.Seal(req.Identities); terr != nil {
			h.logger.Warn("tmpx generation failed, response will omit tmpx",
				"request_id", req.RequestID, "error", terr)
			h.recorder.StageOutcome(ctx, StageTMPX, OutcomeError)
		} else if token != "" {
			resp.Tmpx = token
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

// writeResponse marshals payload to JSON in one shot and writes it. Using
// json.Marshal + w.Write avoids the json.Encoder allocation per request
// that json.NewEncoder(w).Encode would incur on the hot path.
//
// Returns true on success and false when either marshal or write failed —
// the caller stamps this onto the request-completion metric so a write
// failure shows up distinctly from "ok".
func (h *identityHandler) writeResponse(w http.ResponseWriter, resp *tmproto.IdentityMatchResponse) bool {
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

func (h *identityHandler) recordCompletion(ctx context.Context, start time.Time, status string) {
	h.recorder.RequestCompleted(ctx, status, time.Since(start))
}
