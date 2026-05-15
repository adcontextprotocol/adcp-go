package scope3

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_RequiresURLAndToken(t *testing.T) {
	_, err := New("", "token")
	assert.Error(t, err)
	_, err = New("https://example", "")
	assert.Error(t, err)
}

func TestWithHTTPTimeout_DoesNotMutateCustomClient(t *testing.T) {
	custom := &http.Client{Timeout: 7 * time.Second}
	// WithHTTPTimeout applied AFTER WithHTTPClient must not change the
	// caller's client's Timeout — the caller's client is authoritative.
	src, err := New("https://example", "tok",
		WithHTTPClient(custom),
		WithHTTPTimeout(123*time.Millisecond),
	)
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, custom.Timeout, "caller-supplied client Timeout must not be mutated")
	assert.Same(t, custom, src.client, "Source must use the caller-supplied client")
}

func TestWithHTTPTimeout_SetsDefaultClientTimeout(t *testing.T) {
	src, err := New("https://example", "tok", WithHTTPTimeout(123*time.Millisecond))
	require.NoError(t, err)
	assert.Equal(t, 123*time.Millisecond, src.client.Timeout)
}

func TestLoadAll_SendsBearerAndParsesResponse(t *testing.T) {
	var receivedAuth atomic.Value
	var receivedBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth.Store(r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		receivedBody.Store(string(body))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"lastUpdatedAt": "2026-05-13T10:42:43.123456789Z",
			"targetingConfigs": [
				{
					"sellerAgentUrl": "https://seller.example/agent",
					"packageId": "pkg-1",
					"targetSegments": {
						"allOf": ["high_intent"],
						"anyOf": ["cooking_fans", "home_improvement"],
						"noneOf": ["competitor"]
					}
				},
				{
					"sellerAgentUrl": "https://seller.example/agent",
					"packageId": "pkg-2"
				}
			],
			"removedTargetingConfigs": []
		}`))
	}))
	defer srv.Close()

	src, err := New(srv.URL, "secret-token")
	require.NoError(t, err)

	snap, err := src.LoadAll(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "Bearer secret-token", receivedAuth.Load())
	body, _ := receivedBody.Load().(string)
	assert.NotContains(t, body, `"after"`, "LoadAll must not send an `after` field")

	assert.Equal(t, time.Date(2026, 5, 13, 10, 42, 43, 123456789, time.UTC), snap.LastUpdatedAt)
	require.Len(t, snap.Configs, 2)

	byKey := make(map[identityconfig.Key]identityconfig.Entry, len(snap.Configs))
	for _, e := range snap.Configs {
		byKey[e.Key] = e
	}

	pkg1 := byKey[identityconfig.Key{SellerAgentURL: "https://seller.example/agent", PackageID: "pkg-1"}]
	require.NotNil(t, pkg1.TargetSegments)
	assert.Equal(t, []string{"high_intent"}, pkg1.TargetSegments.AllOf)
	assert.Equal(t, []string{"cooking_fans", "home_improvement"}, pkg1.TargetSegments.AnyOf)
	assert.Equal(t, []string{"competitor"}, pkg1.TargetSegments.NoneOf)

	pkg2 := byKey[identityconfig.Key{SellerAgentURL: "https://seller.example/agent", PackageID: "pkg-2"}]
	assert.Nil(t, pkg2.TargetSegments, "missing targetSegments unmarshals as nil rule")
}

func TestLoadUpdatedAfter_SendsAfterField(t *testing.T) {
	after := time.Date(2026, 5, 13, 10, 0, 0, 500_000_000, time.UTC)
	var receivedBody atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody.Store(string(body))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"lastUpdatedAt": "2026-05-13T11:00:00Z",
			"targetingConfigs": [
				{"sellerAgentUrl": "s", "packageId": "p", "targetSegments": {"anyOf": ["x"]}}
			],
			"removedTargetingConfigs": [
				{"sellerAgentUrl": "s", "packageId": "gone"}
			]
		}`))
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	delta, err := src.LoadUpdatedAfter(context.Background(), after)
	require.NoError(t, err)

	body, _ := receivedBody.Load().(string)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	gotAfter, ok := parsed["after"].(string)
	require.True(t, ok, "after should be a string")
	gotTime, err := time.Parse(time.RFC3339Nano, gotAfter)
	require.NoError(t, err)
	assert.True(t, gotTime.Equal(after), "after field should round-trip the supplied time, got %s", gotAfter)

	require.Len(t, delta.Upserted, 1)
	require.Len(t, delta.Removed, 1)
	assert.Equal(t, identityconfig.Key{SellerAgentURL: "s", PackageID: "gone"}, delta.Removed[0])
	assert.True(t, delta.LastUpdatedAt.Equal(time.Date(2026, 5, 13, 11, 0, 0, 0, time.UTC)))
}

func TestLoad_HTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	_, err = src.LoadAll(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

func TestLoad_ContextCancellationStopsRequest(t *testing.T) {
	// Handler sleeps longer than the client's deadline. The client must
	// return promptly when its context times out rather than waiting on the
	// handler.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	src, err := New(srv.URL, "tok")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = src.LoadAll(ctx)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.Less(t, elapsed, time.Second, "LoadAll should return promptly after context cancellation")
}
