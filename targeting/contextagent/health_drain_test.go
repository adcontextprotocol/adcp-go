package contextagent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealthEndpoint_DrainReturns503 pins the spec §Health requirement
// that /health MUST return 503 when the process has flipped its
// readiness signal (drain window before shutdown). Previously /health
// always returned 200, so orchestrators could not distinguish
// draining-but-still-alive from ready-to-serve and continued to route
// traffic at pods mid-shutdown.
func TestHealthEndpoint_DrainReturns503(t *testing.T) {
	// Load-balance-facing /health mounts on the request listener; use
	// the same wiring path as NewServer to exercise the real handler.
	running := true
	srv := NewServer(ServerConfig{
		Port:           8081,
		ContextHandler: http.NotFoundHandler(),
		AdminPort:      0,
		IsRunning:      func() bool { return running },
	})
	require.NotNil(t, srv)

	// Ready → 200
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// Draining → 503
	running = false
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
