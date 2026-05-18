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
)

func TestContentTypeMiddleware_RejectsNonJSON(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, true)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if called {
		t.Fatal("inner handler should not be called for non-JSON content type")
	}
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnsupportedMediaType)
	}
	if !strings.Contains(rr.Body.String(), "application/json") {
		t.Fatalf("body should mention application/json: %s", rr.Body.String())
	}
}

func TestContentTypeMiddleware_AcceptsJSONWithCharset(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, true)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Fatal("inner handler should be called for application/json")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestContentTypeMiddleware_DisabledIsPassThrough(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	h := contentTypeMiddleware(inner, false)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", strings.NewReader(""))
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !called {
		t.Fatal("inner handler should be called when strict=false")
	}
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

	if rec.count.Load() != 1 {
		t.Fatalf("HandlerPanic count = %d, want 1", rec.count.Load())
	}
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body not JSON: %v / %s", err, rr.Body.String())
	}
	if body["code"] != string("internal_error") {
		t.Fatalf("body.code = %q, want internal_error", body["code"])
	}
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

	if sawCtxID != "abc-123" {
		t.Fatalf("ctx request id = %q, want abc-123", sawCtxID)
	}
	if got := rr.Header().Get("X-Request-ID"); got != "abc-123" {
		t.Fatalf("response header = %q, want abc-123", got)
	}
}

func TestRequestIDMiddleware_MissingHeaderNoEcho(t *testing.T) {
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	h := requestIDMiddleware(inner)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got != "" {
		t.Fatalf("response should not echo a missing request id, got %q", got)
	}
}

func TestAccessLogMiddleware_Disabled(t *testing.T) {
	var captured bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&captured, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	h := accessLogMiddleware(inner, false, logger)

	req := httptest.NewRequest(http.MethodPost, "/tmp/identity", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if captured.Len() != 0 {
		t.Fatalf("disabled middleware should not log; got %q", captured.String())
	}
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
	if !strings.Contains(out, "status=202") {
		t.Fatalf("expected status=202 in log line, got %q", out)
	}
	if !strings.Contains(out, `request_id=req-xyz`) {
		t.Fatalf("expected request_id=req-xyz in log line, got %q", out)
	}
}
