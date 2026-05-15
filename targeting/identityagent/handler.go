package identityagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// identityHandler is the http.Handler for POST /tmp/identity. It decodes an
// IdentityMatchRequest under a hard request-timeout budget, runs
// Service.Evaluate, optionally seals a TMPX token, and writes the
// IdentityMatchResponse. Budget overruns produce a 200 response with an
// empty EligiblePackageIDs slice — the same shape callers receive for any
// other fail-closed outcome, so SDKs don't need a special branch.
//
// The handler is wrapped by TMP signature verification at a higher layer
// (see ServeMux assembly in server.go).
type identityHandler struct {
	service        *Service
	tmpxCfg        *tmpxConfig
	requestTimeout time.Duration
	recorder       Recorder
	logger         *slog.Logger
	ttlSec         int
}

// IdentityHandlerConfig packages the inputs for NewIdentityHandler.
type IdentityHandlerConfig struct {
	Service        *Service
	TMPXConfig     *tmpxConfig
	RequestTimeout time.Duration
	Recorder       Recorder
	Logger         *slog.Logger
	TTLSeconds     int
}

const (
	defaultResponseTTLSec = 60

	// maxRequestBodyBytes bounds the request body. Identity requests are
	// small JSON objects; anything larger is rejected at decode time.
	maxRequestBodyBytes = 64 * 1024
)

// NewIdentityHandler returns the http.Handler for POST /tmp/identity.
func NewIdentityHandler(cfg IdentityHandlerConfig) http.Handler {
	if cfg.Recorder == nil {
		cfg.Recorder = noopRecorder{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	ttl := cfg.TTLSeconds
	if ttl <= 0 {
		ttl = defaultResponseTTLSec
	}
	return &identityHandler{
		service:        cfg.Service,
		tmpxCfg:        cfg.TMPXConfig,
		requestTimeout: cfg.RequestTimeout,
		recorder:       cfg.Recorder,
		logger:         cfg.Logger,
		ttlSec:         ttl,
	}
}

func (h *identityHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h.recorder.RequestStarted(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout)
	defer cancel()

	var req tmproto.IdentityMatchRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err := dec.Decode(&req); err != nil {
		h.writeError(w, "", http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "request body is not valid JSON")
		h.recordCompletion(ctx, start, "bad_request")
		return
	}
	if err := tmproto.ValidateIdentityRequest(&req); err != nil {
		h.writeError(w, req.RequestID, http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, err.Error())
		h.recordCompletion(ctx, start, "bad_request")
		return
	}

	result := h.service.Evaluate(ctx, &req)

	// Fail closed on budget overrun: return the standard wire shape with no
	// eligible packages, matching what callers see for any other fail-closed
	// outcome. RequestID is preserved so the buyer can correlate.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		h.writeResponse(w, &tmproto.IdentityMatchResponse{
			RequestID: req.RequestID,
			TTLSec:    h.ttlSec,
		})
		h.recordCompletion(ctx, start, "timeout")
		return
	}

	eligible := make([]string, 0, len(result.Eligibility))
	for _, e := range result.Eligibility {
		if e.Eligible {
			eligible = append(eligible, e.PackageID)
		}
	}

	resp := &tmproto.IdentityMatchResponse{
		RequestID:          result.RequestID,
		EligiblePackageIDs: eligible,
		TTLSec:             h.ttlSec,
	}
	if h.tmpxCfg != nil && len(eligible) > 0 {
		tmpxStart := time.Now()
		if token, terr := buildTmpxToken(h.tmpxCfg, req.Identities); terr != nil {
			h.logger.Warn("tmpx generation failed, response will omit tmpx",
				"request_id", req.RequestID, "error", terr)
			h.recorder.StageOutcome(ctx, StageTMPX, OutcomeError)
		} else if token != "" {
			resp.Tmpx = token
			h.recorder.StageOutcome(ctx, StageTMPX, OutcomePass)
		}
		h.recorder.StageDuration(ctx, StageTMPX, time.Since(tmpxStart))
	}

	h.writeResponse(w, resp)
	h.logger.Debug("identity match",
		"request_id", req.RequestID,
		"packages", len(req.PackageIDs),
		"eligible", len(eligible),
		"latency_ms", time.Since(start).Milliseconds())
	h.recordCompletion(ctx, start, "ok")
}

// writeResponse marshals payload to JSON in one shot and writes it. Using
// json.Marshal + w.Write avoids the json.Encoder allocation per request
// that json.NewEncoder(w).Encode would incur on the hot path.
func (h *identityHandler) writeResponse(w http.ResponseWriter, resp *tmproto.IdentityMatchResponse) {
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(resp)
	if err != nil {
		h.logger.Warn("failed to marshal identity response", "request_id", resp.RequestID, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(body); err != nil {
		h.logger.Warn("failed to write identity response", "request_id", resp.RequestID, "error", err)
	}
}

func (h *identityHandler) writeError(w http.ResponseWriter, requestID string, status int, code tmproto.ErrorCode, message string) {
	body, err := json.Marshal(tmproto.ErrorResponse{
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
