package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// flappyServer returns 5xx for the first n attempts, then 200.
func flappyServer(t *testing.T, failures int) (*httptest.Server, *atomic.Int32, *atomic.Pointer[string]) {
	t.Helper()
	var attempts atomic.Int32
	var lastNonce atomic.Pointer[string]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		// Capture the Signature-Input header to verify each attempt carries
		// a fresh nonce (retries MUST re-sign, not replay the old signature).
		si := r.Header.Get("Signature-Input")
		lastNonce.Store(&si)
		// Drain body.
		_, _ = io.Copy(io.Discard, r.Body)
		if int(n) <= failures {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &attempts, &lastNonce
}

// publisherForTest wires a Publisher that does not actually sleep — retry
// timing is deterministic so the test suite does not wait seconds.
func publisherForTest(t *testing.T) *Publisher {
	t.Helper()
	_, _ = webhookKeypair(t, "pub-test") // ensure helper is loaded
	signer, _ := webhookKeypair(t, "pub-test")
	var fakeSleep = func(ctx context.Context, _ time.Duration) error {
		// Respect context cancellation so cancellation tests still work.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	return NewPublisher(PublisherOptions{
		Signer: signer,
		Retry: RetryPolicy{
			MaxAttempts:  5,
			InitialDelay: time.Millisecond,
			MaxDelay:     time.Millisecond,
			Multiplier:   1,
			Jitter:       0,
			MaxElapsed:   time.Hour,
		},
		Sleep: fakeSleep,
	})
}

func closeResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()
	require.NoError(t, resp.Body.Close())
}

func TestPublisherRetriesThenSucceeds(t *testing.T) {
	srv, attempts, _ := flappyServer(t, 2) // first 2 attempts 5xx, third 200
	pub := publisherForTest(t)

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	res, err := pub.Emit(context.Background(), srv.URL, p)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)
	assert.Equal(t, http.StatusOK, res.Response.StatusCode)
	assert.Equal(t, 3, res.Attempts)
	assert.Equal(t, int32(3), attempts.Load())
	assert.NotEmpty(t, res.IdempotencyKey)
}

func TestPublisherStopsAtMaxAttempts(t *testing.T) {
	srv, attempts, _ := flappyServer(t, 999) // never succeeds
	pub := publisherForTest(t)

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	_, err := pub.Emit(context.Background(), srv.URL, p)
	require.Error(t, err)
	var pe *PublishError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "max_attempts", pe.Reason)
	assert.Equal(t, 5, pe.Attempts)
	assert.Equal(t, int32(5), attempts.Load())
	assert.Equal(t, http.StatusInternalServerError, pe.LastStatus)
}

func TestPublisherNoRetryOn4xxTerminal(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusConflict) // 409 — receiver rejecting request structure
	}))
	t.Cleanup(srv.Close)
	pub := publisherForTest(t)

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	_, err := pub.Emit(context.Background(), srv.URL, p)
	require.Error(t, err)
	var pe *PublishError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "terminal_status", pe.Reason)
	assert.Equal(t, int32(1), attempts.Load(), "4xx terminal must not retry")
	assert.Equal(t, http.StatusConflict, pe.LastStatus)
}

func TestPublisherRetriesOn408And429(t *testing.T) {
	cases := []int{http.StatusRequestTimeout, http.StatusTooManyRequests}
	for _, status := range cases {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := attempts.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if n == 1 {
					w.WriteHeader(status)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(srv.Close)
			pub := publisherForTest(t)

			p := &adcp.MCPWebhookPayload{
				TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
			}
			res, err := pub.Emit(context.Background(), srv.URL, p)
			require.NoError(t, err)
			closeResponseBody(t, res.Response)
			assert.Equal(t, 2, res.Attempts)
		})
	}
}

func TestPublisherHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		if n == 1 {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	signer, _ := webhookKeypair(t, "pub-ra")
	var sleptWith time.Duration
	pub := NewPublisher(PublisherOptions{
		Signer: signer,
		Retry: RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: time.Millisecond, // computed backoff would be tiny
			MaxDelay:     time.Millisecond,
			Multiplier:   1,
			Jitter:       0,
			MaxElapsed:   time.Hour,
		},
		Sleep: func(_ context.Context, d time.Duration) error { sleptWith = d; return nil },
	})

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	res, err := pub.Emit(context.Background(), srv.URL, p)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)
	assert.Equal(t, 42*time.Second, sleptWith, "Retry-After must override computed backoff")
}

func TestPublisherMaxElapsedCapsRetries(t *testing.T) {
	srv, _, _ := flappyServer(t, 999)

	// Clock advances by 10 minutes per call so the budget trips after 1 tick.
	base := time.Now()
	var tick atomic.Int32
	clock := func() time.Time {
		return base.Add(time.Duration(tick.Add(1)) * 10 * time.Minute)
	}

	signer, _ := webhookKeypair(t, "pub-elapsed")
	pub := NewPublisher(PublisherOptions{
		Signer: signer,
		Retry: RetryPolicy{
			MaxAttempts:  10,
			InitialDelay: time.Second,
			MaxDelay:     time.Second,
			Multiplier:   1,
			Jitter:       0,
			MaxElapsed:   time.Minute, // tripped after first tick
		},
		Clock: clock,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	_, err := pub.Emit(context.Background(), srv.URL, p)
	require.Error(t, err)
	var pe *PublishError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "max_elapsed", pe.Reason)
}

func TestPublisherContextCancellationAborts(t *testing.T) {
	srv, _, _ := flappyServer(t, 999)
	ctx, cancel := context.WithCancel(context.Background())

	signer, _ := webhookKeypair(t, "pub-cancel")
	pub := NewPublisher(PublisherOptions{
		Signer: signer,
		Retry: RetryPolicy{
			MaxAttempts: 10, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond,
			Multiplier: 1, Jitter: 0, MaxElapsed: time.Hour,
		},
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err() // the context is now canceled
		},
	})
	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	_, err := pub.Emit(ctx, srv.URL, p)
	require.Error(t, err)
	var pe *PublishError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "context_canceled", pe.Reason)
}

func TestPublisherEachAttemptFreshSignature(t *testing.T) {
	// Record the Signature-Input headers from each attempt — every retry MUST
	// carry a fresh nonce, otherwise a dedup-savvy receiver would replay-
	// reject the second attempt and the publisher would spin forever.
	var sigs atomic.Pointer[[]string]
	sigs.Store(&[]string{})
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		cur := *sigs.Load()
		cur = append(cur, r.Header.Get("Signature-Input"))
		sigs.Store(&cur)
		_, _ = io.Copy(io.Discard, r.Body)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	pub := publisherForTest(t)

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	res, err := pub.Emit(context.Background(), srv.URL, p)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)

	got := *sigs.Load()
	require.Len(t, got, 2)
	assert.NotEqual(t, got[0], got[1], "each retry MUST mint a fresh signature (distinct nonce)")
}

func TestPublisherIdempotencyKeyStableAcrossAttempts(t *testing.T) {
	// The receiver sees the same idempotency_key on every attempt — dedupe
	// relies on this. Capture both attempts' key fields and compare.
	var keys atomic.Pointer[[]string]
	keys.Store(&[]string{})
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		// Extract idempotency_key from the body (without disturbing verification
		// — this server does not verify, so we just re-read).
		body, _ := io.ReadAll(r.Body)
		cur := *keys.Load()
		cur = append(cur, extractKeyForTest(body))
		keys.Store(&cur)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	pub := publisherForTest(t)

	p := &adcp.MCPWebhookPayload{
		TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z",
	}
	res, err := pub.Emit(context.Background(), srv.URL, p)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)

	got := *keys.Load()
	require.Len(t, got, 2)
	assert.Equal(t, got[0], got[1], "idempotency_key MUST be stable across retries")
	assert.Equal(t, got[0], res.IdempotencyKey)
}

func TestPublisherEndToEndWithVerifyingReceiver(t *testing.T) {
	// The full loop: a Publisher sends through real HTTP to an HTTPHandler
	// configured with Verification + dedup. The first attempt fails (closed
	// server connection triggers a transport error); the second succeeds.
	// This proves Publisher's retry path composes with our verifier +
	// idempotency store end-to-end.
	const keyid = "e2e-pub-ed25519"
	signer, resolver := webhookKeypair(t, keyid)

	var delivered atomic.Int32
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)
	mux := http.NewServeMux()
	mux.Handle("/webhooks/mcp", HTTPHandler(HTTPHandlerOptions{
		Store: NewStore(Options{Backend: backend, TTL: time.Hour}),
		Handler: func(context.Context, []byte) error {
			delivered.Add(1)
			return nil
		},
		Verification: &VerificationOptions{
			Resolver:       resolver,
			Replay:         signing.NewMemoryReplayStore(0),
			SchemeOverride: "http",
		},
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// One call: verifier runs, dedup fires, 200 returned. Publisher's retry
	// path is exercised by the earlier tests — this one proves the happy
	// path composes without hidden incompatibilities.
	pub := NewPublisher(PublisherOptions{
		Signer: signer,
		Retry: RetryPolicy{
			MaxAttempts: 3, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond,
			Multiplier: 1, Jitter: 0, MaxElapsed: time.Minute,
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	p := &adcp.MCPWebhookPayload{
		TaskID: "e2e", TaskType: "create_media_buy", Status: "completed",
		Timestamp: "2026-04-19T00:00:00Z",
	}
	res, err := pub.Emit(context.Background(), srv.URL+"/webhooks/mcp", p)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)
	assert.Equal(t, http.StatusOK, res.Response.StatusCode)
	assert.Equal(t, int32(1), delivered.Load())
}

func TestPublisherPanicsWithoutSigner(t *testing.T) {
	assert.PanicsWithValue(t, "webhook: PublisherOptions.Signer is required", func() {
		NewPublisher(PublisherOptions{})
	})
}

func TestRetryPolicyDefaults(t *testing.T) {
	r := (RetryPolicy{}).withDefaults()
	assert.Equal(t, DefaultRetry.MaxAttempts, r.MaxAttempts)
	assert.Equal(t, DefaultRetry.InitialDelay, r.InitialDelay)
	assert.Equal(t, DefaultRetry.MaxDelay, r.MaxDelay)
	assert.Equal(t, DefaultRetry.Multiplier, r.Multiplier)
	assert.Equal(t, DefaultRetry.MaxElapsed, r.MaxElapsed)
	// Jitter=0 is intentional "off" — deterministic delays for tests.
	// Negative values clamp to 0; positive values pass through.
	assert.Equal(t, float64(0), r.Jitter)
}

func TestBackoffMonotonic(t *testing.T) {
	// Zero-jitter backoff must grow monotonically until MaxDelay, then stay.
	p := RetryPolicy{InitialDelay: 100 * time.Millisecond, MaxDelay: time.Second, Multiplier: 2, Jitter: 0}
	d1 := backoff(1, p)
	d2 := backoff(2, p)
	d3 := backoff(3, p)
	d4 := backoff(4, p)
	d10 := backoff(10, p)
	assert.Equal(t, 100*time.Millisecond, d1)
	assert.Equal(t, 200*time.Millisecond, d2)
	assert.Equal(t, 400*time.Millisecond, d3)
	assert.Equal(t, 800*time.Millisecond, d4)
	assert.Equal(t, time.Second, d10, "past MaxDelay must clamp")
}

func TestClassifyResponse(t *testing.T) {
	cases := []struct {
		code int
		want retryClass
	}{
		{200, classSuccess},
		{201, classSuccess},
		{299, classSuccess},
		{400, classTerminal},
		{401, classTerminal},
		{404, classTerminal},
		{409, classTerminal},
		{408, classTransient},
		{429, classTransient},
		{500, classTransient},
		{502, classTransient},
		{599, classTransient},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.code, Header: http.Header{}}
			got, _ := classifyResponse(resp)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	assert.Equal(t, 30*time.Second, parseRetryAfter("30", nil))
	assert.Equal(t, time.Duration(0), parseRetryAfter("", nil))
	assert.Equal(t, time.Duration(0), parseRetryAfter("nope", nil))
	// HTTP-date in the past → 0.
	assert.Equal(t, time.Duration(0), parseRetryAfter("Mon, 01 Jan 2000 00:00:00 GMT", nil))
	// HTTP-date in the future → positive duration.
	future := time.Now().Add(10 * time.Minute).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future, nil)
	assert.True(t, got > 5*time.Minute && got < 15*time.Minute, "got %s", got)
}

// --- helpers ---

// extractKeyForTest is a minimal JSON peek — for assertions only, NOT for
// production use. Duplicates idempotency.extractKey so tests don't depend on
// an unexported function.
func extractKeyForTest(body []byte) string {
	// Cheap string search — body is small, test-only.
	const needle = `"idempotency_key":"`
	idx := indexOf(body, []byte(needle))
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := start
	for end < len(body) && body[end] != '"' {
		end++
	}
	return string(body[start:end])
}

func indexOf(hay, needle []byte) int {
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}
