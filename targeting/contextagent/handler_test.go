package contextagent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestContextHandlerValidationErrorIsGenericAndLogged pins the
// generic-error-message invariant from AGENTS.md ("Never echo err.Error()
// in HTTP responses") for the context-match validation path. The same
// pattern was fixed for router.HandleContextMatch and identityagent's
// ServeHTTP in the validator-error sweep (adcontextprotocol/adcp-go#190,
// #201; landed in PR #210) but that sweep did not touch this handler,
// which kept echoing tmproto.ValidateContextRequest's err.Error() text —
// e.g. "property_id contains invalid characters" — straight into the HTTP
// response body.
func TestContextHandlerValidationErrorIsGenericAndLogged(t *testing.T) {
	var logs bytes.Buffer
	h := NewHandler(HandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	body := `{
		"type": "context_match_request",
		"request_id": "ctx-invalid",
		"property_rid": "rid-1",
		"property_id": "bad:property",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	req := httptest.NewRequest(http.MethodPost, "/context", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, resp.Code)
	assert.Equal(t, "ctx-invalid", resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), "property_id")

	logText := logs.String()
	assert.Contains(t, logText, "invalid context-match request")
	assert.Contains(t, logText, `"method":"POST"`)
	assert.Contains(t, logText, `"path":"/context"`)
	assert.Contains(t, logText, "ctx-invalid")
	assert.Contains(t, logText, "property_id contains invalid characters")
}

// TestContextHandlerInvalidRequestIDIsNotEchoed pins the companion
// invariant: a request_id that itself fails validateSafeID (so it is
// unsafe to echo — see tmproto.SafeRequestIDForEcho) must not appear in
// the HTTP response body, and is elided from the structured log too.
func TestContextHandlerInvalidRequestIDIsNotEchoed(t *testing.T) {
	var logs bytes.Buffer
	h := NewHandler(HandlerConfig{
		RequestTimeout:             time.Second,
		RequestBodyLimit:           64 * 1024,
		ResponseTTL:                time.Minute,
		SupportedADCPMajorVersions: []int{3},
		Logger:                     slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	body := `{
		"type": "context_match_request",
		"request_id": "bad/id",
		"property_rid": "rid-1",
		"property_id": "pub-1",
		"property_type": "website",
		"placement_id": "sidebar",
		"seller_agent_url": "https://seller.example.com/agent"
	}`
	req := httptest.NewRequest(http.MethodPost, "/context", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	var resp tmproto.ErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.RequestID)
	assert.Equal(t, "invalid request", resp.Message)
	assert.NotContains(t, w.Body.String(), "bad/id")

	logText := logs.String()
	assert.Contains(t, logText, "invalid context-match request")
	assert.Contains(t, logText, `"request_id_valid":false`)
	assert.NotContains(t, logText, "bad/id")
}
