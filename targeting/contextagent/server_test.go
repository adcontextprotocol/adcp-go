package contextagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// fakeRecorder counts the calls the server-path middleware makes so
// the httptest harness can assert "RequestCompleted with status=X
// fired exactly once" without standing up a Prometheus registry.
type fakeRecorder struct {
	mu                  sync.Mutex
	requestsStarted     int
	requestsCompleted   []requestCompleted
	handlerPanicCount   int
	backgroundPanic     int
	storeErrors         []string
	stageOutcomes       []stageOutcome
	keystoreOutcomes    []string
}

type requestCompleted struct {
	Status   string
	Duration time.Duration
}

type stageOutcome struct {
	Stage   string
	Outcome string
}

func (f *fakeRecorder) RequestStarted(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestsStarted++
}

func (f *fakeRecorder) RequestCompleted(_ context.Context, status string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requestsCompleted = append(f.requestsCompleted, requestCompleted{Status: status, Duration: d})
}

func (f *fakeRecorder) StageOutcome(_ context.Context, stage, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stageOutcomes = append(f.stageOutcomes, stageOutcome{Stage: stage, Outcome: outcome})
}

func (f *fakeRecorder) StageDuration(context.Context, string, time.Duration) {}

func (f *fakeRecorder) StoreError(_ context.Context, store string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.storeErrors = append(f.storeErrors, store)
}

func (f *fakeRecorder) KeystoreRefresh(_ context.Context, outcome string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keystoreOutcomes = append(f.keystoreOutcomes, outcome)
}

func (f *fakeRecorder) HandlerPanic(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlerPanicCount++
}

func (f *fakeRecorder) BackgroundPanic(context.Context, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backgroundPanic++
}

func (f *fakeRecorder) completedStatus(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.requestsCompleted, 1, "expected exactly one RequestCompleted")
	return f.requestsCompleted[0].Status
}

// newTestServer builds a *http.Server suitable for httptest with the
// supplied /context inner handler, leaving signature verification and
// strict content-type off so each test exercises one middleware in
// isolation. AdminPort==0 keeps /live and /metrics on the same mux.
func newTestServer(t *testing.T, ctxInner http.Handler, rec Recorder, checks []LivenessCheck, running func() bool) *httptest.Server {
	t.Helper()
	srv := NewServer(ServerConfig{
		ContextHandler:    ctxInner,
		IsRunning:         running,
		Version:           "test-version",
		LivenessChecks:    checks,
		Recorder:          rec,
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       2 * time.Second,
		MaxHeaderBytes:    8 * 1024,
		RequestBodyLimit:  256 * 1024,
	})
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestRequestMetricsMiddleware_RecordsStatusFromInnerHandler(t *testing.T) {
	cases := []struct {
		name       string
		writeCode  int
		writeBody  bool
		wantStatus string
	}{
		{"200_no_explicit_writeheader", 0, true, StatusOK},
		{"200_explicit", http.StatusOK, true, StatusOK},
		{"400_client_error", http.StatusBadRequest, true, StatusClientError},
		{"500_server_error", http.StatusInternalServerError, true, StatusServerError},
		{"504_timeout", http.StatusGatewayTimeout, true, StatusTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeRecorder{}
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.writeCode != 0 {
					w.WriteHeader(tc.writeCode)
				}
				if tc.writeBody {
					_, _ = w.Write([]byte(`{"ok":true}`))
				}
			})
			ts := newTestServer(t, inner, rec, nil, nil)
			resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(`{}`))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			assert.Equal(t, tc.wantStatus, rec.completedStatus(t))
			rec.mu.Lock()
			assert.Equal(t, 1, rec.requestsStarted, "RequestStarted must fire exactly once per request")
			rec.mu.Unlock()
		})
	}
}

func TestRecoverMiddleware_PanicProduces500WithErrorResponseBody(t *testing.T) {
	rec := &fakeRecorder{}
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	ts := newTestServer(t, inner, rec, nil, nil)

	resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, _ := io.ReadAll(resp.Body)
	var er tmproto.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &er))
	assert.Equal(t, tmproto.TypeError, er.Type)
	assert.Equal(t, tmproto.ErrorCodeInternalError, er.Code)
	assert.Equal(t, "handler panic", er.Message)

	rec.mu.Lock()
	assert.Equal(t, 1, rec.handlerPanicCount, "HandlerPanic must fire exactly once per panic")
	// requestMetricsMiddleware sits *outside* the recover wrapper on
	// the /context chain, so the completed status is the 500 the
	// recover handler wrote.
	require.Len(t, rec.requestsCompleted, 1)
	assert.Equal(t, StatusServerError, rec.requestsCompleted[0].Status)
	rec.mu.Unlock()
}

func TestRecoverMiddleware_ErrAbortHandlerRePanics(t *testing.T) {
	rec := &fakeRecorder{}
	innerCalled := atomic.Bool{}
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		innerCalled.Store(true)
		panic(http.ErrAbortHandler)
	})
	// Wrap the inner directly so this test doesn't depend on the
	// full server middleware ordering — http.ErrAbortHandler must
	// be re-raised by recoverMiddleware so net/http's outer handler
	// suppresses the response, instead of writing a JSON 500.
	wrapped := recoverMiddleware(inner, rec, nil)
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err == nil {
		_ = resp.Body.Close()
	}
	// Either the client got EOF / connection-reset, or net/http
	// produced no body. The contract is "no JSON 500" — assert the
	// inner ran and HandlerPanic did NOT fire (the sentinel path
	// rethrows before recorder.HandlerPanic is called).
	assert.True(t, innerCalled.Load(), "inner must have been invoked")
	rec.mu.Lock()
	assert.Zero(t, rec.handlerPanicCount,
		"ErrAbortHandler sentinel must NOT count as a handler panic")
	rec.mu.Unlock()
}

func TestLive_DegradedWhenCheckFails(t *testing.T) {
	running := func() bool { return true }
	checks := []LivenessCheck{
		{Name: "suppression_snapshot", Fn: func() error { return errors.New("3 consecutive failures") }},
		{Name: "ok-check", Fn: func() error { return nil }},
	}
	ts := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil, checks, running)

	resp, err := http.Get(ts.URL + "/live")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "degraded", parsed["status"])
	assert.Equal(t, "test-version", parsed["version"])
	failures, ok := parsed["failures"].(map[string]any)
	require.True(t, ok, "failures map must be present and an object")
	assert.Contains(t, failures, "suppression_snapshot")
	assert.NotContains(t, failures, "ok-check", "passing checks must not appear in failures map")
}

func TestLive_ShuttingDownTakesPrecedenceOverDegraded(t *testing.T) {
	// IsRunning=false beats a failing liveness check: the orchestrator
	// should stop sending traffic before considering pod recycling.
	running := func() bool { return false }
	checks := []LivenessCheck{
		{Name: "suppression_snapshot", Fn: func() error { return errors.New("would-fail") }},
	}
	ts := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil, checks, running)

	resp, err := http.Get(ts.URL + "/live")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]string
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "shutting_down", parsed["status"])
}

func TestLive_OKWhenRunningAndAllChecksPass(t *testing.T) {
	running := func() bool { return true }
	checks := []LivenessCheck{
		{Name: "ok-1", Fn: func() error { return nil }},
		{Name: "ok-2", Fn: func() error { return nil }},
	}
	ts := newTestServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil, checks, running)

	resp, err := http.Get(ts.URL + "/live")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var parsed map[string]string
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, "ok", parsed["status"])
	assert.Equal(t, "test-version", parsed["version"])
}

func TestRecordingResponseWriter_WriteBeforeWriteHeaderDefaultsTo200(t *testing.T) {
	rec := &fakeRecorder{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Call Write without an explicit WriteHeader — net/http (and
		// our recordingResponseWriter) must default the status to 200.
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	ts := newTestServer(t, inner, rec, nil, nil)
	resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	assert.Equal(t, StatusOK, rec.completedStatus(t))
}
