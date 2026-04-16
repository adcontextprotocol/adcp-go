package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchFeed_OK(t *testing.T) {
	cursor := "019414a0-0000-7000-0000-000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/registry/feed", r.URL.Path)
		assert.Equal(t, cursor, r.URL.Query().Get("cursor"))
		assert.Equal(t, "agent.*,property.*", r.URL.Query().Get("types"))
		assert.Equal(t, "500", r.URL.Query().Get("limit"))
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{
			Events: []FeedEvent{
				{EventID: "019414a0-0000-7000-0000-000000000002", EventType: "agent.discovered", EntityType: "agent", EntityID: "https://agent.example.com", Actor: "pipeline:crawler"},
			},
			Cursor:  strPtr("019414a0-0000-7000-0000-000000000002"),
			HasMore: false,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	resp, err := c.FetchFeed(context.Background(), cursor, []string{"agent.*", "property.*"}, 500)
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, "agent.discovered", resp.Events[0].EventType)
	assert.False(t, resp.HasMore)
}

func TestFetchFeed_NoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.URL.Query().Get("cursor"), "cursor should not be set for initial fetch")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{Events: []FeedEvent{}, HasMore: false})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	resp, err := c.FetchFeed(context.Background(), "", nil, 0)
	require.NoError(t, err)
	assert.Empty(t, resp.Events)
}

func TestFetchFeed_CursorExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(FeedError{
			Error:   "cursor_expired",
			Message: "Cursor is older than 90-day retention window.",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	_, err := c.FetchFeed(context.Background(), "old-cursor", nil, 0)

	var expired *CursorExpiredError
	require.True(t, errors.As(err, &expired), "err = %T (%v), want *CursorExpiredError", err, err)
	assert.Equal(t, "Cursor is older than 90-day retention window.", expired.Message)
}

func TestFetchFeed_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "bad-token")
	_, err := c.FetchFeed(context.Background(), "", nil, 0)

	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr), "err = %T, want *APIError", err)
	assert.Equal(t, 401, apiErr.StatusCode)
}

func TestSearchAgents_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/registry/agents/search", r.URL.Path)
		q := r.URL.Query()
		assert.Equal(t, "ctv,olv", q.Get("channels"))
		assert.Equal(t, "US", q.Get("markets"))
		assert.Equal(t, "true", q.Get("has_tmp"))
		assert.Equal(t, "10", q.Get("limit"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SearchResponse{
			Results: []AgentProfile{
				{AgentURL: "https://agent.example.com", Channels: []string{"ctv"}, Markets: []string{"US"}, HasTMP: true, PropertyCount: 42},
			},
			HasMore: false,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	hasTMP := true
	resp, err := c.SearchAgents(context.Background(), SearchParams{
		Channels: []string{"ctv", "olv"},
		Markets:  []string{"US"},
		HasTMP:   &hasTMP,
		Limit:    10,
	})
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, 42, resp.Results[0].PropertyCount)
}

func TestRequestCrawl_Accepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/registry/crawl-request", r.URL.Path)

		var req CrawlRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		assert.Equal(t, "example.com", req.Domain)

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(CrawlResponse{Message: "Crawl request accepted", Domain: "example.com"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	resp, err := c.RequestCrawl(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Equal(t, "example.com", resp.Domain)
}

func TestRequestCrawl_RateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":       "Rate limit exceeded: 5 minutes per domain",
			"retry_after": 180,
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	_, err := c.RequestCrawl(context.Background(), "example.com")

	var rl *RateLimitError
	require.True(t, errors.As(err, &rl), "err = %T (%v), want *RateLimitError", err, err)
	assert.Equal(t, 180, rl.RetryAfter)
}

func TestNewClient_NoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "Authorization header should not be set")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{Events: []FeedEvent{}, HasMore: false})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.FetchFeed(context.Background(), "", nil, 0)
	require.NoError(t, err)
}

func strPtr(s string) *string { return &s }
