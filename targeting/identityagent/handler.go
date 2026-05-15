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

// IdentityHandler returns an http.Handler that decodes an
// IdentityMatchRequest, runs Service.Evaluate, optionally seals a TMPX
// token, and writes the IdentityMatchResponse. Every request is bounded by
// the requestTimeout supplied at construction; a request that doesn't
// complete within that budget returns 504 with code "internal_error".
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

const defaultResponseTTLSec = 60

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

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		h.writeError(w, "", http.StatusBadRequest, tmproto.ErrorCodeInvalidRequest, "failed to read request body")
		h.recordCompletion(ctx, start, "bad_request")
		return
	}

	var req tmproto.IdentityMatchRequest
	if err := json.Unmarshal(body, &req); err != nil {
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

	if err := ctx.Err(); errors.Is(err, context.DeadlineExceeded) {
		h.writeError(w, req.RequestID, http.StatusGatewayTimeout, tmproto.ErrorCodeInternalError, "request budget exceeded")
		h.recordCompletion(ctx, start, "timeout")
		return
	}

	var eligible []string
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

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Warn("failed to write identity response", "request_id", req.RequestID, "error", err)
	}
	h.logger.Debug("identity match",
		"request_id", req.RequestID,
		"packages", len(req.PackageIDs),
		"eligible", len(eligible),
		"latency_ms", time.Since(start).Milliseconds())
	h.recordCompletion(ctx, start, "ok")
}

func (h *identityHandler) writeError(w http.ResponseWriter, requestID string, status int, code tmproto.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
}

func (h *identityHandler) recordCompletion(ctx context.Context, start time.Time, status string) {
	h.recorder.RequestCompleted(ctx, status, time.Since(start))
}
