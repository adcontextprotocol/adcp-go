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


// HandlerConfig packages the inputs for NewHandler.
type HandlerConfig struct {
	Engine                     *targeting.ContextEngine
	RequestTimeout             time.Duration
	RequestBodyLimit           int64
	ResponseTTL                time.Duration
	SupportedADCPMajorVersions []int
	Logger                     *slog.Logger
}

// NewHandler returns the http.Handler for POST /context.
func NewHandler(cfg HandlerConfig) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
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
		logger:           logger,
	}
}

type handler struct {
	engine           *targeting.ContextEngine
	requestTimeout   time.Duration
	requestBodyLimit int64
	responseTTL      time.Duration
	supportedVers    map[int]struct{}
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
		writeError(w, tmproto.SafeRequestIDForEcho(req.RequestID), tmproto.ErrorCodeInvalidRequest, err.Error(), http.StatusBadRequest)
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
	if h.responseTTL > 0 {
		// ContextMatchResponse.cache_ttl has no spec-level maximum,
		// only a 5-minute default the router applies when omitted.
		// Don't borrow IdentityMatchResponse's 300s serve_window_sec
		// cap — that's a buyer-asserted serve throttle, a different
		// concept on a different message type.
		resp.CacheTTL = int(h.responseTTL.Seconds())
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Warn("response encode failed", "request_id", req.RequestID, "error", err)
	}
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
