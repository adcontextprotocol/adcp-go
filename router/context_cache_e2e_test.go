package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseCacheRequest(requestID string) map[string]any {
	return map[string]any{
		"$schema":          "https://schemas.example/context-a.json",
		"adcp_version":     "3.2",
		"type":             tmproto.TypeContextMatchRequest,
		"request_id":       requestID,
		"property_rid":     "rid-cache-full",
		"property_id":      "publisher-site",
		"property_type":    "website",
		"placement_id":     "article-rail",
		"seller_agent_url": "https://seller.example/agent",
		"artifact": map[string]any{
			"property_rid": "rid-cache-full",
			"artifact_id":  "article-42",
			"assets": []any{map[string]any{
				"type": "text", "role": "body", "content": "baseline artifact",
			}},
		},
		"artifact_refs": []any{
			map[string]any{"type": "url", "value": "https://publisher.example/article/42"},
			map[string]any{"type": "isbn", "value": "9780131103627"},
		},
		"context_signals": map[string]any{
			"topics": []any{"632", "633"}, "taxonomy_id": 7, "sentiment": "neutral", "language": "en",
		},
		"geo":         map[string]any{"country": "US", "region": "US-CA"},
		"package_ids": []any{"pkg-a", "pkg-b"},
	}
}

func cloneRequestMap(t *testing.T, src map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(src)
	require.NoError(t, err)
	var dst map[string]any
	require.NoError(t, json.Unmarshal(raw, &dst))
	return dst
}

func serveContextRequest(t *testing.T, r *Router, body map[string]any) tmproto.ContextMatchResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	r.HandleContextMatch(w, httptest.NewRequest(http.MethodPost, "/tmp/context", bytes.NewReader(raw)))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var response tmproto.ContextMatchResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&response))
	return response
}

func TestRouterContextCache_ContextHashSeparatesEveryForwardedDimension(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{
			Type: tmproto.TypeContextMatchResponse, Offers: []tmproto.Offer{{PackageID: "response-" + string(rune('A'+n-1))}},
		})
	}))
	defer provider.Close()

	r := testRouter([]ProviderConfig{{
		ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second, CacheNamespace: "authz1-packages1-model1-rules1",
	}})
	r.contextCache = NewContextCache(time.Hour)
	base := baseCacheRequest("request-one")
	first := serveContextRequest(t, r, base)
	require.Equal(t, int32(1), calls.Load())
	require.Len(t, first.Offers, 1)

	ignored := cloneRequestMap(t, base)
	ignored["request_id"] = "request-two"
	ignored["$schema"] = "https://schemas.example/context-b.json"
	hit := serveContextRequest(t, r, ignored)
	assert.Equal(t, int32(1), calls.Load(), "only request_id and $schema differences must hit")
	assert.Equal(t, "request-two", hit.RequestID, "a hit must echo the current request_id")
	assert.Equal(t, first.Offers, hit.Offers)

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"artifact", func(m map[string]any) { m["artifact"].(map[string]any)["artifact_id"] = "article-43" }},
		{"artifact refs", func(m map[string]any) {
			m["artifact_refs"].([]any)[0].(map[string]any)["value"] = "https://publisher.example/article/43"
		}},
		{"artifact refs order", func(m map[string]any) {
			refs := m["artifact_refs"].([]any)
			m["artifact_refs"] = []any{refs[1], refs[0]}
		}},
		{"context signals", func(m map[string]any) { m["context_signals"].(map[string]any)["sentiment"] = "positive" }},
		{"context signal array order", func(m map[string]any) { m["context_signals"].(map[string]any)["topics"] = []any{"633", "632"} }},
		{"geo", func(m map[string]any) { m["geo"].(map[string]any)["region"] = "US-NY" }},
		{"package ids", func(m map[string]any) { m["package_ids"] = []any{"pkg-a", "pkg-c"} }},
		{"package id order", func(m map[string]any) { m["package_ids"] = []any{"pkg-b", "pkg-a"} }},
		{"seller", func(m map[string]any) { m["seller_agent_url"] = "https://other-seller.example/agent" }},
		{"property rid", func(m map[string]any) {
			m["property_rid"] = "rid-cache-other"
			m["artifact"].(map[string]any)["property_rid"] = "rid-cache-other"
		}},
		{"property id", func(m map[string]any) { m["property_id"] = "publisher-other" }},
		{"property type", func(m map[string]any) { m["property_type"] = "mobile_app" }},
		{"placement", func(m map[string]any) { m["placement_id"] = "article-footer" }},
		{"protocol version", func(m map[string]any) { m["protocol_version"] = "1.0" }},
		{"adcp version", func(m map[string]any) { m["adcp_version"] = "3.1" }},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := cloneRequestMap(t, base)
			request["request_id"] = "separation-request"
			tt.mutate(request)
			serveContextRequest(t, r, request)
			assert.Equal(t, int32(i+2), calls.Load(), "%s must produce a distinct context_hash", tt.name)
		})
	}
}

func TestRouterContextCache_PartitionsProviders(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{Type: tmproto.TypeContextMatchResponse})
	}))
	defer provider.Close()
	r := testRouter([]ProviderConfig{
		{ID: "provider_a", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second, CacheNamespace: "generation-1"},
		{ID: "provider_b", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second, CacheNamespace: "generation-1"},
	})
	r.contextCache = NewContextCache(time.Hour)
	serveContextRequest(t, r, baseCacheRequest("one"))
	require.Equal(t, int32(2), calls.Load())
	serveContextRequest(t, r, baseCacheRequest("two"))
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, 2, r.contextCache.Size())
}

func TestRouterContextCache_SafeDefaultAndCurrentAuthorization(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{Type: tmproto.TypeContextMatchResponse})
	}))
	defer provider.Close()

	t.Run("missing namespace bypasses", func(t *testing.T) {
		calls.Store(0)
		r := testRouter([]ProviderConfig{{ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second}})
		r.contextCache = NewContextCache(time.Hour)
		serveContextRequest(t, r, baseCacheRequest("one"))
		serveContextRequest(t, r, baseCacheRequest("two"))
		assert.Equal(t, int32(2), calls.Load())
		assert.Equal(t, 0, r.contextCache.Size())
	})

	t.Run("request authorization denial cannot flush provider cache", func(t *testing.T) {
		calls.Store(0)
		var authorized atomic.Bool
		authorized.Store(true)
		r := testRouter([]ProviderConfig{{ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second}})
		r.contextCache = NewContextCache(time.Hour)
		r.contextNamespaceResolver = func(context.Context, ProviderConfig) ContextCacheNamespaceResolution {
			if !authorized.Load() {
				return ContextCacheNamespaceResolution{Status: ContextCacheNamespaceBypass}
			}
			return ContextCacheNamespaceResolution{Namespace: "authz1-packages1-model1-rules1", Status: ContextCacheNamespaceReady}
		}
		serveContextRequest(t, r, baseCacheRequest("one"))
		require.Equal(t, 1, r.contextCache.Size())
		authorized.Store(false)
		serveContextRequest(t, r, baseCacheRequest("two"))
		assert.Equal(t, int32(2), calls.Load(), "a warm hit must still pass current authorization state")
		assert.Equal(t, 1, r.contextCache.Size(), "request-local denial must not purge the provider generation")
		authorized.Store(true)
		serveContextRequest(t, r, baseCacheRequest("three"))
		assert.Equal(t, int32(2), calls.Load(), "an authorized caller must retain its existing warm entry")
	})

	t.Run("unknown global generation invalidates and requires rotation", func(t *testing.T) {
		calls.Store(0)
		var state atomic.Int32
		r := testRouter([]ProviderConfig{{ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second}})
		r.contextCache = NewContextCache(time.Hour)
		r.contextNamespaceResolver = func(context.Context, ProviderConfig) ContextCacheNamespaceResolution {
			switch state.Load() {
			case 0, 2:
				return ContextCacheNamespaceResolution{Namespace: "generation-1", Status: ContextCacheNamespaceReady}
			case 1:
				return ContextCacheNamespaceResolution{Status: ContextCacheNamespaceUnknown}
			default:
				return ContextCacheNamespaceResolution{Namespace: "generation-2", Status: ContextCacheNamespaceReady}
			}
		}
		serveContextRequest(t, r, baseCacheRequest("one"))
		require.Equal(t, 1, r.contextCache.Size())
		state.Store(1)
		serveContextRequest(t, r, baseCacheRequest("unknown"))
		assert.Equal(t, 0, r.contextCache.Size(), "unknown provider generation must purge existing entries")
		state.Store(2)
		serveContextRequest(t, r, baseCacheRequest("reused"))
		assert.Equal(t, int32(3), calls.Load(), "the invalidated generation must not be reused")
		state.Store(3)
		serveContextRequest(t, r, baseCacheRequest("rotated"))
		serveContextRequest(t, r, baseCacheRequest("rotated-warm"))
		assert.Equal(t, int32(4), calls.Load(), "a novel generation may cache again")
	})
}

func TestRouterContextCache_ProviderFilteringPrecedesContextHash(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		var forwarded tmproto.ContextMatchRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&forwarded))
		assert.Equal(t, []string{"pkg-a"}, forwarded.PackageIDs)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{Type: tmproto.TypeContextMatchResponse})
	}))
	defer provider.Close()
	r := testRouter([]ProviderConfig{{
		ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: time.Second,
		CacheNamespace: "generation-1", PackageIDs: []string{"pkg-a"},
	}})
	r.contextCache = NewContextCache(time.Hour)
	one := baseCacheRequest("one")
	one["package_ids"] = []any{"pkg-a", "not-forwarded-one"}
	two := baseCacheRequest("two")
	two["package_ids"] = []any{"pkg-a", "not-forwarded-two"}
	serveContextRequest(t, r, one)
	serveContextRequest(t, r, two)
	assert.Equal(t, int32(1), calls.Load(), "provider-equivalent forwarded requests must share a cache entry")
}

func TestRouterContextCache_RotationRejectsOldInflightInsertion(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
		}
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{
			Type: tmproto.TypeContextMatchResponse, Offers: []tmproto.Offer{{PackageID: map[bool]string{true: "old", false: "new"}[n == 1]}},
		})
	}))
	defer provider.Close()

	var mu sync.RWMutex
	namespace := "generation-1"
	r := testRouter([]ProviderConfig{{ID: "prov", Endpoint: provider.URL, ContextMatch: true, Timeout: 5 * time.Second}})
	r.contextCache = NewContextCache(time.Hour)
	r.contextNamespaceResolver = func(context.Context, ProviderConfig) ContextCacheNamespaceResolution {
		mu.RLock()
		defer mu.RUnlock()
		return ContextCacheNamespaceResolution{Namespace: namespace, Status: ContextCacheNamespaceReady}
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		serveContextRequest(t, r, baseCacheRequest("old-request"))
	}()
	<-started
	mu.Lock()
	namespace = "generation-2"
	mu.Unlock()
	newResponse := serveContextRequest(t, r, baseCacheRequest("new-request"))
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, "new", newResponse.Offers[0].PackageID)
	close(release)
	<-firstDone

	warm := serveContextRequest(t, r, baseCacheRequest("warm-request"))
	assert.Equal(t, int32(2), calls.Load(), "the old completion must not overwrite or invalidate the new generation")
	require.Equal(t, "new", warm.Offers[0].PackageID)
}

func TestRouterContextCache_EndpointReplacementAndSensitiveDataStayOutOfLogs(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	oldProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oldCalls.Add(1)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{Type: tmproto.TypeContextMatchResponse})
	}))
	defer oldProvider.Close()
	newProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		newCalls.Add(1)
		_ = json.NewEncoder(w).Encode(tmproto.ProviderContextMatchResponse{Type: tmproto.TypeContextMatchResponse})
	}))
	defer newProvider.Close()

	const namespace = "sensitive-namespace-marker"
	var logs bytes.Buffer
	oldConfig := ProviderConfig{ID: "prov", Endpoint: oldProvider.URL, ContextMatch: true, Timeout: time.Second, CacheNamespace: namespace}
	r := testRouter([]ProviderConfig{oldConfig})
	r.contextCache = NewContextCache(time.Hour)
	r.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	request := baseCacheRequest("request-marker")
	request["artifact"].(map[string]any)["artifact_id"] = "sensitive-request-preimage-marker"
	serveContextRequest(t, r, request)

	newConfig := oldConfig
	newConfig.Endpoint = newProvider.URL
	r.providers.Swap([]ProviderConfig{newConfig})
	serveContextRequest(t, r, request)
	assert.Equal(t, int32(1), oldCalls.Load())
	assert.Equal(t, int32(1), newCalls.Load(), "endpoint replacement must rotate provider evaluation context")
	r.providers.Swap(nil)
	r.providers.Swap([]ProviderConfig{newConfig})
	serveContextRequest(t, r, request)
	assert.Equal(t, int32(2), newCalls.Load(), "provider removal and re-add must not resurrect an old entry")
	assert.NotContains(t, logs.String(), namespace)
	assert.NotContains(t, logs.String(), "sensitive-request-preimage-marker")
}
