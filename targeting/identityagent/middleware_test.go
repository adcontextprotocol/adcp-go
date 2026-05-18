package identityagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentTypeMiddleware_RejectsNonJSON(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, true)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.False(t, called, "inner handler should not be called for non-JSON content type")
	assert.Equal(t, http.StatusUnsupportedMediaType, rr.Code)
	assert.Contains(t, rr.Body.String(), "application/json")
}

func TestContentTypeMiddleware_AcceptsJSONWithCharset(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, true)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.True(t, called, "inner handler should be called for application/json")
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestContentTypeMiddleware_DisabledIsPassThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, false)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.True(t, called, "inner handler should be called when strict=false")
}

func TestRecoverMiddleware_TrapsPanic(t *testing.T) {
	rec := &countingPanicRecorder{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	})
	h := recoverMiddleware(inner, rec, logger)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, int64(1), rec.count.Load(), "HandlerPanic count")
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "response body must be JSON: %s", rr.Body.String())
	assert.Equal(t, "internal_error", body["code"])
}

// countingPanicRecorder is a Recorder whose HandlerPanic call is observable.
type countingPanicRecorder struct {
	noopRecorder
	count atomic.Int64
}

func (c *countingPanicRecorder) HandlerPanic(context.Context) { c.count.Add(1) }

func TestRequestIDMiddleware_EchoesHeader(t *testing.T) {
	var sawCtxID string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		sawCtxID = requestIDFromRequest(r)
	})
	h := requestIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, "abc-123", sawCtxID, "ctx request id")
	assert.Equal(t, "abc-123", rr.Header().Get("X-Request-ID"), "response header")
}

func TestRequestIDMiddleware_MissingHeaderNoEcho(t *testing.T) {
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	h := requestIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Empty(t, rr.Header().Get("X-Request-ID"), "response should not echo a missing request id")
}

func TestAccessLogMiddleware_Disabled(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&captured, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := accessLogMiddleware(inner, false, logger)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Zero(t, captured.Len(), "disabled middleware should not log: %q", captured.String())
}

func TestAccessLogMiddleware_EnabledEmitsOnce(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&captured, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		_, _ = w.Write([]byte("ok"))
	})
	h := accessLogMiddleware(inner, true, logger)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	req.Header.Set("X-Request-ID", "req-xyz")
	rr := httptest.NewRecorder()
	// The access log reads the request id from context, which is set by
	// the request-id middleware in production. Test the composition by
	// wrapping with both.
	composed := requestIDMiddleware(h)
	composed.ServeHTTP(rr, req)

	out := captured.String()
	assert.Contains(t, out, "status=202")
	assert.Contains(t, out, "request_id=req-xyz")
}
