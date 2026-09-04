package contextagent

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


// HandlerConfig packages the inputs for NewHandler. Recorder is
// accepted for symmetry with identity-agent and future per-stage
// instrumentation; request lifecycle is observed by
// requestMetricsMiddleware, so the handler itself only consults
// recorder for outcomes the middleware can't see (e.g. unknown
// adcp_major_version rejected before any engine call).
type HandlerConfig struct {
	Engine                     *targeting.ContextEngine
	RequestTimeout             time.Duration
	RequestBodyLimit           int64
	ResponseTTL                time.Duration
	SupportedADCPMajorVersions []int
	Recorder                   Recorder
	Logger                     *slog.Logger
}

// NewHandler returns the http.Handler for POST /context.
func NewHandler(cfg HandlerConfig) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	recorder := cfg.Recorder
	if recorder == nil {
		recorder = noopRecorder{}
	}
	supported := make(map[int]struct{}, len(cfg.SupportedADCPMajorVersions))
	for _, v := range cfg.SupportedADCPMajorVersions {
		supported[v] = struct{}{}
	}
	return &handler{
		engine:           cfg.Engine,
		requestTimeout:   cfg.RequestTimeout,
		requestBodyLimit: cfg.RequestBodyLimit,
		responseTTL:      cfg.ResponseTTL,
		supportedVers:    supported,
		recorder:         recorder,
		logger:           logger,
	}
}

type handler struct {
	engine           *targeting.ContextEngine
	requestTimeout   time.Duration
	requestBodyLimit int64
	responseTTL      time.Duration
	supportedVers    map[int]struct{}
	recorder         Recorder
	logger           *slog.Logger
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "POST required", http.StatusMethodNotAllowed)
		return
	}

	body := http.MaxBytesReader(w, r.Body, h.requestBodyLimit)
	defer func() { _ = body.Close() }()

	// DisallowUnknownFields enforces the schema's
	// additionalProperties: false invariant on ContextMatchRequest.
	// The signed-request path is covered by the verifier's strict
	// decode in tmproto.VerifyContextMatchHandler; this branch
	// closes the unsigned/dev path so silently-accepted extension
	// fields can't slip past either gate.
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	var req tmproto.ContextMatchRequest
	if err := dec.Decode(&req); err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeError(w, "", tmproto.ErrorCodeInvalidRequest, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeError(w, "", tmproto.ErrorCodeInvalidRequest, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := tmproto.ValidateContextRequest(&req); err != nil {
		h.logValidationFailure(r, req.RequestID, err)
		writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), tmproto.ErrorCodeInvalidRequest, "invalid request", http.StatusBadRequest)
		return
	}

	if req.AdcpMajorVersion != 0 {
		if _, ok := h.supportedVers[req.AdcpMajorVersion]; !ok {
			writeError(w, req.RequestID, tmproto.ErrorCodeInvalidRequest,
				"unsupported adcp_major_version", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	if h.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.requestTimeout)
		defer cancel()
	}

	result, err := h.engine.Evaluate(ctx, &req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			writeError(w, req.RequestID, tmproto.ErrorCodeInternalError, "request deadline exceeded", http.StatusGatewayTimeout)
			return
		}
		h.logger.Error("context engine returned error",
			"request_id", req.RequestID, "error", err)
		writeError(w, req.RequestID, tmproto.ErrorCodeInternalError, "internal error", http.StatusInternalServerError)
		return
	}

	resp := tmproto.ContextMatchResponse{
		Type:      tmproto.TypeContextMatchResponse,
		RequestID: result.RequestID,
		Offers:    result.Offers,
		Signals:   result.Signals,
	}
	// ContextMatchResponse.cache_ttl has a schema-enforced maximum of
	// 86400 seconds (see adcp/schemas/trusted-match/context-match-response.json)
	// and the router applies a 5-minute default when the field is
	// omitted. Don't borrow IdentityMatchResponse's 300s
	// serve_window_sec cap — that's a buyer-asserted serve throttle,
	// a different concept on a different message type. The field is
	// a *int so omission is distinguishable from an explicit 0
	// (which the spec defines as "disable caching"); we only assign
	// when we have a positive whole-second TTL. A configured
	// RESPONSE_TTL between 0 and 1s truncates to zero seconds;
	// emitting cache_ttl=0 in that case would tell the router to
	// disable caching entirely, which is the opposite of the
	// operator's intent — so we omit the field instead and let the
	// router's default apply.
	if secs := int(h.responseTTL.Seconds()); secs > 0 {
		resp.CacheTTL = &secs
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Warn("response encode failed", "request_id", req.RequestID, "error", err)
	}
}

// logValidationFailure logs a rejected request's validation error
// server-side with request context, mirroring identityagent's
// logValidationFailure. The HTTP response only ever gets the generic
// "invalid request" message (see ServeHTTP) — validator error text
// (which can include request-shaped detail like field names and
// lengths) must not cross the response boundary per AGENTS.md's
// generic-error-message invariant. request_id is only logged when it
// passes SafeRequestIDForEcho; an id that fails that check is elided
// and request_id_valid=false is logged instead, so a control-byte or
// oversized request_id doesn't get written verbatim into logs either.
func (h *handler) logValidationFailure(r *http.Request, requestID string, err error) {
	attrs := []any{"method", r.Method, "path", r.URL.Path, "error", err}
	if safeID := tmproto.SafeRequestIDForEcho(requestID); safeID != "" {
		attrs = append(attrs, "request_id", safeID)
	} else if requestID != "" {
		attrs = append(attrs, "request_id_valid", false)
	}
	h.logger.Warn("invalid context-match request", attrs...)
}

// writeError writes a TMP error response. Headers must be set before
// WriteHeader because anything after WriteHeader is silently dropped
// by net/http.
func writeError(w http.ResponseWriter, requestID string, code tmproto.ErrorCode, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
		Type:      tmproto.TypeError,
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
}
