package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// TestEndToEndDeliverToHTTPHandler exercises the full publisher → subscriber
// flow over a real TCP listener: webhook.NewSigner + webhook.Deliver on one
// side, webhook.HTTPHandler with Verification on the other. The retry assertion
// proves dedup fires even though each Deliver call produces a fresh signature.
func TestEndToEndDeliverToHTTPHandler(t *testing.T) {
	const keyid = "e2e-ed25519"
	signer, resolver := webhookKeypair(t, keyid)

	var received atomic.Int32
	var receivedBody []byte
	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)

	mux := http.NewServeMux()
	mux.Handle("/webhooks/mcp", HTTPHandler(HTTPHandlerOptions{
		Store: NewStore(Options{Backend: backend, TTL: time.Hour}),
		Handler: func(_ context.Context, body []byte) error {
			received.Add(1)
			receivedBody = append([]byte(nil), body...)
			return nil
		},
		Verification: &VerificationOptions{
			Resolver: resolver,
			Replay:   signing.NewMemoryReplayStore(0),
			// Real TCP listener is plain http://; override scheme so the
			// verifier reconstructs @target-uri the same way the signer did.
			SchemeOverride: "http",
		},
	}))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Deliver installs CheckRedirect on the client's behalf when nil is passed.
	p := &adcp.MCPWebhookPayload{
		TaskID:    "task_e2e",
		TaskType:  "create_media_buy",
		Status:    "completed",
		Timestamp: "2026-04-19T00:00:00Z",
	}

	// First delivery: handler runs.
	ctx := context.Background()
	res, err := Deliver(ctx, srv.URL+"/webhooks/mcp", p, signer, nil)
	require.NoError(t, err)
	closeResponseBody(t, res.Response)
	require.Equal(t, http.StatusOK, res.Response.StatusCode)
	require.Equal(t, int32(1), received.Load())
	require.Equal(t, res.Body, receivedBody, "handler must observe the exact signed bytes")
	require.Equal(t, p.IdempotencyKey, res.IdempotencyKey, "Deliver must return the same key it stamped into p")

	// Retry with the same payload: Marshal preserves the existing
	// idempotency_key, Deliver mints a fresh signature, dedup fires.
	res2, err := Deliver(ctx, srv.URL+"/webhooks/mcp", p, signer, nil)
	require.NoError(t, err)
	closeResponseBody(t, res2.Response)
	require.Equal(t, http.StatusOK, res2.Response.StatusCode)
	require.Equal(t, int32(1), received.Load(), "dedup must suppress handler on retry")
	assert.Equal(t, res.Body, res2.Body, "retries MUST resend byte-identical bodies")
	assert.Equal(t, res.IdempotencyKey, res2.IdempotencyKey)
}

// TestEndToEndConflictDifferentPayloadSameKey proves a sender bug (same
// idempotency_key, different body) is caught at the receiver.
func TestEndToEndConflictDifferentPayloadSameKey(t *testing.T) {
	const keyid = "e2e-conflict-ed25519"
	signer, resolver := webhookKeypair(t, keyid)

	backend := idempotency.NewMemoryBackend(0)
	t.Cleanup(backend.Close)

	mux := http.NewServeMux()
	mux.Handle("/webhooks/mcp", HTTPHandler(HTTPHandlerOptions{
		Store:   NewStore(Options{Backend: backend, TTL: time.Hour}),
		Handler: func(_ context.Context, _ []byte) error { return nil },
		Verification: &VerificationOptions{
			Resolver:       resolver,
			Replay:         signing.NewMemoryReplayStore(0),
			SchemeOverride: "http",
		},
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	key := idempotency.Generate()
	p1 := &adcp.MCPWebhookPayload{IdempotencyKey: key, TaskID: "t1", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z"}
	p2 := &adcp.MCPWebhookPayload{IdempotencyKey: key, TaskID: "t2", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z"}

	ctx := context.Background()
	res1, err := Deliver(ctx, srv.URL+"/webhooks/mcp", p1, signer, nil)
	require.NoError(t, err)
	closeResponseBody(t, res1.Response)
	require.Equal(t, http.StatusOK, res1.Response.StatusCode)

	res2, err := Deliver(ctx, srv.URL+"/webhooks/mcp", p2, signer, nil)
	require.NoError(t, err)
	closeResponseBody(t, res2.Response)
	assert.Equal(t, http.StatusConflict, res2.Response.StatusCode, "same key + different body MUST conflict")
}
