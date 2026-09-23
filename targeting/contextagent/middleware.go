package contextagent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// recoverMiddleware traps panics raised by downstream handlers, records
// them on the supplied Recorder, logs the stack at ERROR, and writes a
// JSON-shaped 500 so the client doesn't see a truncated body or Go's
// default panic page.
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
			// response from inside a handler; net/http suppresses
			// the stack and so should we.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			logger.Error("handler panic",
				"path", r.URL.Path,
				"method", r.Method,
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

var panicResponseBody = func() []byte {
	body, _ := json.Marshal(tmproto.ErrorResponse{
		Type:    tmproto.TypeError,
		Code:    tmproto.ErrorCodeInternalError,
		Message: "handler panic",
	})
	return body
}()

// recordingResponseWriter wraps http.ResponseWriter so the request
// metrics middleware can observe the final status after the handler
// returns. Status defaults to 200 when WriteHeader is never called
// explicitly.
//
// semanticStatus is a handler-set override for cases where the HTTP
// status code alone does not encode the request-level outcome. TMP
// application errors travel on HTTP 200 with an error envelope, so
// deriving the status label purely from the code (via
// statusFromHTTPCode) would label every timeout and internal_error as
// "ok" — breaking the agent's own timeout- and error-rate alerts.
// Handlers set the semantic status via setSemanticStatus before
// writing the 200 + error envelope; the middleware prefers it over
// the code-derived label when non-empty. Values are the same bounded
// StatusOK / StatusTimeout / StatusServerError / StatusClientError
// enum the middleware would otherwise emit.
type recordingResponseWriter struct {
	http.ResponseWriter
	status         int
	semanticStatus string
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
	return w.ResponseWriter.Write(b)
}

// setSemanticStatus records the outcome label the middleware should
// prefer over the code-derived label. Called by the handler on paths
// that intentionally return 200 with a TMP error envelope. No-op when
// w is not a *recordingResponseWriter (test harnesses without the
// middleware chain) — the metric would already be a noopRecorder in
// that case, so silently dropping the override is safe.
func setSemanticStatus(w http.ResponseWriter, status string) {
	if rw, ok := w.(*recordingResponseWriter); ok {
		rw.semanticStatus = status
	}
}

// requestMetricsMiddleware emits one RequestStarted on entry and one
// RequestCompleted on exit, with a status label derived from the final
// HTTP status. The handler chain owns the body and status codes; this
// wrapper just observes them.
//
// Operator PromQL note: in-flight requests are
// `<ns>_requests_started_total - sum(<ns>_request_duration_seconds_count)`
// summed across statuses, because RequestCompleted feeds the histogram
// (whose `_count` series counts samples), not a paired completed
// counter. Pairing the two metrics is intentional: histogram samples
// give per-status latency, the started counter gives unlabeled inflow.
func requestMetricsMiddleware(next http.Handler, recorder Recorder) http.Handler {
	if recorder == nil {
		recorder = noopRecorder{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.RequestStarted(r.Context())
		start := time.Now()
		rw := &recordingResponseWriter{ResponseWriter: w}
		// Defer the completion record so a downstream panic (caught
		// by the inner recoverMiddleware, which writes the 500 to rw)
		// still observes a final status. Without the defer, a panic
		// would skip RequestCompleted entirely and operators would
		// see RequestStarted climb without a matching completion —
		// looking like a stuck request instead of a recovered panic.
		defer func() {
			status := rw.semanticStatus
			if status == "" {
				status = statusFromHTTPCode(rw.status)
			}
			recorder.RequestCompleted(r.Context(), status, time.Since(start))
		}()
		next.ServeHTTP(rw, r)
	})
}

// statusFromHTTPCode maps an HTTP status to one of the bounded
// request-status labels. 0 (handler never wrote a header) is treated
// as 200 because net/http would synthesize one on Write. 1xx and 3xx
// also fall through to StatusOK — neither can happen on this server's
// request path today, and labelling a stray 3xx as "ok" beats inventing
// a new label.
func statusFromHTTPCode(code int) string {
	switch {
	case code == 0, code >= 200 && code < 300:
		return StatusOK
	case code == http.StatusGatewayTimeout:
		return StatusTimeout
	case code >= 500:
		return StatusServerError
	case code >= 400:
		return StatusClientError
	default:
		return StatusOK
	}
}
