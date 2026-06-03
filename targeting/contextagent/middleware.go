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
type recordingResponseWriter struct {
	http.ResponseWriter
	status int
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

// requestMetricsMiddleware emits one RequestStarted on entry and one
// RequestCompleted on exit, with a status label derived from the final
// HTTP status. The handler chain owns the body and status codes; this
// wrapper just observes them.
func requestMetricsMiddleware(next http.Handler, recorder Recorder) http.Handler {
	if recorder == nil {
		recorder = noopRecorder{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.RequestStarted(r.Context())
		start := time.Now()
		rw := &recordingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r)
		recorder.RequestCompleted(r.Context(), statusFromHTTPCode(rw.status), time.Since(start))
	})
}

// statusFromHTTPCode maps an HTTP status to one of the bounded
// request-status labels. 0 (handler never wrote a header) is treated as
// 200 because net/http would synthesize one on Write.
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
