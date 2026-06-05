package identityagent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// requestIDHeader is the canonical inbound/outbound header for cross-service
// request correlation.
const requestIDHeader = "X-Request-ID"

// recoverMiddleware traps panics raised by downstream handlers, records
// them on the supplied Recorder, logs the stack at ERROR level, and writes
// a JSON-shaped 500 response so the client doesn't see a truncated body or
// a Go default panic page.
func recoverMiddleware(next http.Handler, recorder Recorder, logger *slog.Logger) http.Handler {
	if recorder == nil {
		recorder = noopRecorder{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the documented way to abort a
			// response from inside a handler; net/http suppresses the
			// stack and we should too.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			logger.Error("handler panic",
				"path", r.URL.Path,
				"method", r.Method,
				"request_id", tmproto.SafeRequestIDForEcho(requestIDFromRequest(r)),
				"error", fmt.Sprintf("%v", rec),
				"stack", string(debug.Stack()),
			)
			recorder.HandlerPanic(r.Context())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write(panicResponseBody)
		}()
		next.ServeHTTP(w, r)
	})
}

// panicResponseBody is the canned JSON returned by recoverMiddleware. Fixed
// shape lets clients pattern-match without inspecting a stack trace.
var panicResponseBody = func() []byte {
	body, _ := json.Marshal(tmproto.ErrorResponse{
		Type:    tmproto.TypeError,
		Code:    tmproto.ErrorCodeInternalError,
		Message: "handler panic",
	})
	return body
}()

// requestIDContextKey is the unexported key used to stash the inbound
// X-Request-ID into r.Context() so downstream code (notably the access log
// middleware) can read it without re-parsing headers.
type requestIDContextKey struct{}

// requestIDMiddleware reads X-Request-ID from the inbound request,
// validates it through the same allowlist body-field request_ids
// must pass (validateSafeID via SafeRequestIDForEcho), echoes the
// sanitized value on the response, and stamps it onto r.Context()
// under requestIDContextKey{}. An empty / missing / unsafe header is
// dropped — both the echo and the context value are blank — so a
// malicious caller cannot smuggle control bytes (NUL / BEL / ESC /
// CSI / DEL) past the agent into operator logs or downstream
// services that re-echo the value. The agent does not synthesize a
// substitute ID; assigning one is the buyer's job.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := tmproto.SafeRequestIDForEcho(r.Header.Get(requestIDHeader))
		if id != "" {
			w.Header().Set(requestIDHeader, id)
			r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, id))
		}
		next.ServeHTTP(w, r)
	})
}

// requestIDFromRequest returns the X-Request-ID stamped onto
// r.Context() by requestIDMiddleware, falling back to the raw inbound
// header for callers that run before the middleware (e.g.
// recoverMiddleware in some compositions). Returns "" when no ID is
// available or the available value fails SafeRequestIDForEcho — the
// fallback path is sanitized so a panicking handler whose chain
// hasn't reached the requestID middleware yet still cannot inject
// control bytes into the panic log.
func requestIDFromRequest(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDContextKey{}).(string); ok && v != "" {
		return v
	}
	return tmproto.SafeRequestIDForEcho(r.Header.Get(requestIDHeader))
}

// contentTypeMiddleware rejects any /identity request whose Content-Type is
// not application/json (case-insensitive, parameters ignored) with 415
// Unsupported Media Type. Pass strict=false to disable the check for legacy
// callers — Config.StrictContentType controls this at startup.
func contentTypeMiddleware(next http.Handler, strict bool) http.Handler {
	if !strict {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if idx := strings.IndexByte(ct, ';'); idx >= 0 {
			ct = ct[:idx]
		}
		if !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
			writeJSONErrorResponse(w, "", http.StatusUnsupportedMediaType,
				tmproto.ErrorCodeInvalidRequest,
				"Content-Type must be application/json")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recordingResponseWriter wraps an http.ResponseWriter so accessLogMiddleware
// can read back the final status and response size after the handler
// returns. Status defaults to 200 if WriteHeader is never called explicitly.
type recordingResponseWriter struct {
	http.ResponseWriter
	status  int
	written int64
}

func (w *recordingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// accessLogMiddleware emits one structured INFO line per request when
// enabled. Pass enabled=false to bypass entirely — the middleware is a
// straight pass-through with no recordingResponseWriter wrapping cost.
func accessLogMiddleware(next http.Handler, enabled bool, logger *slog.Logger) http.Handler {
	if !enabled {
		return next
	}
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recordingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"bytes", rw.written,
			"latency_ms", time.Since(start).Milliseconds(),
			"request_id", requestIDFromRequest(r),
		)
	})
}

// writeJSONErrorResponse is the shared error-writer used by middleware
// rejection paths. Kept distinct from the identityHandler.writeError method
// so middleware doesn't depend on the handler type.
func writeJSONErrorResponse(w http.ResponseWriter, requestID string, status int, code tmproto.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, err := json.Marshal(tmproto.ErrorResponse{
		Type:      tmproto.TypeError,
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
	if err != nil {
		return
	}
	_, _ = w.Write(body)
}
