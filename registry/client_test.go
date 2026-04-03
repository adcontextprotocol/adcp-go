package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchFeed_OK(t *testing.T) {
	cursor := "019414a0-0000-7000-0000-000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/registry/feed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("cursor"); got != cursor {
			t.Errorf("cursor = %q, want %q", got, cursor)
		}
		if got := r.URL.Query().Get("types"); got != "agent.*,property.*" {
			t.Errorf("types = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "500" {
			t.Errorf("limit = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth = %q", got)
		}

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
	if err != nil {
		t.Fatalf("FetchFeed: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(resp.Events))
	}
	if resp.Events[0].EventType != "agent.discovered" {
		t.Errorf("event_type = %q", resp.Events[0].EventType)
	}
	if resp.HasMore {
		t.Error("has_more = true, want false")
	}
}

func TestFetchFeed_NoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cursor") != "" {
			t.Error("cursor should not be set for initial fetch")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{Events: []FeedEvent{}, HasMore: false})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	resp, err := c.FetchFeed(context.Background(), "", nil, 0)
	if err != nil {
		t.Fatalf("FetchFeed: %v", err)
	}
	if len(resp.Events) != 0 {
		t.Errorf("events = %d, want 0", len(resp.Events))
	}
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
	if !errors.As(err, &expired) {
		t.Fatalf("err = %T (%v), want *CursorExpiredError", err, err)
	}
	if expired.Message != "Cursor is older than 90-day retention window." {
		t.Errorf("message = %q", expired.Message)
	}
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
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T, want *APIError", err)
	}
	if apiErr.StatusCode != 401 {
		t.Errorf("status = %d, want 401", apiErr.StatusCode)
	}
}

func TestSearchAgents_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/registry/agents/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("channels") != "ctv,olv" {
			t.Errorf("channels = %q", q.Get("channels"))
		}
		if q.Get("markets") != "US" {
			t.Errorf("markets = %q", q.Get("markets"))
		}
		if q.Get("has_tmp") != "true" {
			t.Errorf("has_tmp = %q", q.Get("has_tmp"))
		}
		if q.Get("limit") != "10" {
			t.Errorf("limit = %q", q.Get("limit"))
		}

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
	if err != nil {
		t.Fatalf("SearchAgents: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].PropertyCount != 42 {
		t.Errorf("property_count = %d, want 42", resp.Results[0].PropertyCount)
	}
}

func TestRequestCrawl_Accepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/registry/crawl-request" {
			t.Errorf("path = %s", r.URL.Path)
		}

		var req CrawlRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Domain != "example.com" {
			t.Errorf("domain = %q", req.Domain)
		}

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(CrawlResponse{Message: "Crawl request accepted", Domain: "example.com"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-token")
	resp, err := c.RequestCrawl(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("RequestCrawl: %v", err)
	}
	if resp.Domain != "example.com" {
		t.Errorf("domain = %q", resp.Domain)
	}
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
	if !errors.As(err, &rl) {
		t.Fatalf("err = %T (%v), want *RateLimitError", err, err)
	}
	if rl.RetryAfter != 180 {
		t.Errorf("retry_after = %d, want 180", rl.RetryAfter)
	}
}

func TestNewClient_NoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header should not be set")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FeedResponse{Events: []FeedEvent{}, HasMore: false})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.FetchFeed(context.Background(), "", nil, 0)
	if err != nil {
		t.Fatalf("FetchFeed: %v", err)
	}
}

func strPtr(s string) *string { return &s }
