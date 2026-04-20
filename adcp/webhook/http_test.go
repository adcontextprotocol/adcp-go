package webhook

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/adcontextprotocol/adcp-go/adcp/idempotency"
)

func newTestHTTPHandler(t *testing.T, h Handler, senderID string) http.Handler {
	t.Helper()
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	store := NewStore(Options{Backend: backend, TTL: time.Hour})
	return HTTPHandler(HTTPHandlerOptions{
		Store:           store,
		Handler:         h,
		AllowUnverified: true,
		Sender: func(_ *http.Request) (string, error) {
			return senderID, nil
		},
	})
}

func postJSON(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHTTPHandlerFirstDelivery200(t *testing.T) {
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	var calls atomic.Int32
	h := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }

	rec := postJSON(newTestHTTPHandler(t, h, "sender-A"), body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(1), calls.Load())
}

func TestHTTPHandlerReplay200(t *testing.T) {
	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	var calls atomic.Int32
	h := func(_ context.Context, _ []byte) error { calls.Add(1); return nil }

	handler := newTestHTTPHandler(t, h, "sender-A")
	_ = postJSON(handler, body)
	rec := postJSON(handler, body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int32(1), calls.Load())
}

func TestHTTPHandlerConflict409(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return nil }
	handler := newTestHTTPHandler(t, h, "sender-A")
	_ = postJSON(handler, []byte(`{"idempotency_key":"`+testKey+`","event":"one"}`))
	rec := postJSON(handler, []byte(`{"idempotency_key":"`+testKey+`","event":"two"}`))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHTTPHandlerMissingKey400(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return nil }
	rec := postJSON(newTestHTTPHandler(t, h, "sender-A"), []byte(`{"event":"x"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHTTPHandlerInvalidKey400(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return nil }
	rec := postJSON(newTestHTTPHandler(t, h, "sender-A"), []byte(`{"idempotency_key":"short","event":"x"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHTTPHandlerExpired410(t *testing.T) {
	// Inject a clock into the store so we can cross TTL without sleeping.
	now := time.Unix(1_900_000_000, 0).UTC()
	clock := func() time.Time { return now }
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	store := NewStore(Options{Backend: backend, TTL: time.Hour, ClockSkew: time.Second, Clock: clock})

	handler := HTTPHandler(HTTPHandlerOptions{
		Store:           store,
		Handler:         func(_ context.Context, _ []byte) error { return nil },
		AllowUnverified: true,
		Sender:          func(_ *http.Request) (string, error) { return "sender-A", nil },
	})

	body := []byte(`{"idempotency_key":"` + testKey + `","event":"x"}`)
	rec := postJSON(handler, body)
	assert.Equal(t, http.StatusOK, rec.Code)

	now = now.Add(time.Hour + 30*time.Second)
	rec = postJSON(handler, body)
	assert.Equal(t, http.StatusGone, rec.Code, "past TTL must surface as 410")
}

func TestHTTPHandlerMalformedJSON400(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return nil }
	rec := postJSON(newTestHTTPHandler(t, h, "sender-A"), []byte(`{"idempotency_key":`))
	assert.Equal(t, http.StatusBadRequest, rec.Code, "malformed JSON is a sender bug; 400 stops retry loops")
}

func TestHTTPHandlerUnauthenticated401(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return nil }
	// sender resolver returns "" → unauthenticated.
	handler := newTestHTTPHandler(t, h, "")
	rec := postJSON(handler, []byte(`{"idempotency_key":"`+testKey+`","event":"x"}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTPHandlerPayloadTooLarge413(t *testing.T) {
	backend := idempotency.NewMemoryBackend(0)
	defer backend.Close()
	store := NewStore(Options{Backend: backend, TTL: time.Hour})
	handler := HTTPHandler(HTTPHandlerOptions{
		Store:           store,
		Handler:         func(_ context.Context, _ []byte) error { return nil },
		AllowUnverified: true,
		Sender:          func(_ *http.Request) (string, error) { return "sender-A", nil },
		MaxBodyBytes:    10,
	})
	rec := postJSON(handler, []byte(`{"idempotency_key":"`+testKey+`","event":"x"}`))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestHTTPHandlerHandlerError500(t *testing.T) {
	h := func(_ context.Context, _ []byte) error { return io.ErrUnexpectedEOF }
	rec := postJSON(newTestHTTPHandler(t, h, "sender-A"), []byte(`{"idempotency_key":"`+testKey+`","event":"x"}`))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHTTPHandlerPanicsOnMissingVerification(t *testing.T) {
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	assert.PanicsWithValue(t,
		"webhook: HTTPHandlerOptions.Verification is required, or set AllowUnverified=true and provide a custom Sender (legacy HMAC fallback, removed in AdCP 4.0)",
		func() {
			HTTPHandler(HTTPHandlerOptions{
				Store:   NewStore(Options{Backend: backend, TTL: time.Hour}),
				Handler: func(_ context.Context, _ []byte) error { return nil },
			})
		})
}

func TestHTTPHandlerPanicsOnAllowUnverifiedWithoutSender(t *testing.T) {
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	assert.PanicsWithValue(t,
		"webhook: HTTPHandlerOptions.Sender is required when AllowUnverified=true — SignerSender cannot derive identity without verification",
		func() {
			HTTPHandler(HTTPHandlerOptions{
				Store:           NewStore(Options{Backend: backend, TTL: time.Hour}),
				Handler:         func(_ context.Context, _ []byte) error { return nil },
				AllowUnverified: true,
			})
		})
}
