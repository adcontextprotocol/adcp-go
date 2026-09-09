package contextagent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestContextHandler_DeadlineExceeded_EmitsTMPTimeoutOn200 pins the
// TMP-errors-ride-200 contract on the deadline path: when the engine
// call returns context.DeadlineExceeded / context.Canceled the handler
// MUST return HTTP 200 with an ErrorResponse (type: "error", code:
// timeout) so the router discriminates on the wire type instead of on
// the HTTP status. The prior 504 Gateway Timeout left the router seeing
// a generic "provider returned 504" — losing the timeout vs
// internal_error attribution the spec's error-code enum provides.
func TestContextHandler_DeadlineExceeded_EmitsTMPTimeoutOn200(t *testing.T) {
	// A bare ContextEngine returns ctx.Err() before touching storage,
	// so it's safe to construct without any store wiring and drive it
	// with a pre-cancelled context.
	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{})
	h := NewHandler(HandlerConfig{
		Engine:                     engine,
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewTextHandler(&nopWriter{}, nil)),
	})

	body := `{
		"type": "context_match_request",
		"request_id": "ctx-deadline",
		"property_rid": "rid-1",
		"property_id": "pub-1",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	parentCtx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so engine.Evaluate returns immediately.
	req := httptest.NewRequestWithContext(parentCtx, http.MethodPost, "/context", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "TMP errors ride HTTP 200; a 504 loses code attribution at the router")
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.TypeError, resp.Type)
	assert.Equal(t, tmproto.ErrorCodeTimeout, resp.Code)
	assert.Equal(t, "ctx-deadline", resp.RequestID)
}

// nopWriter silences the handler's logger during test runs.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
