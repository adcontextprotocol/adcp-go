package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"bytes"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sentinel error types for non-2xx responses.

// CursorExpiredError is returned when the feed cursor is older than the
// 90-day retention window (HTTP 410).
type CursorExpiredError struct {
	Message string
}

func (e *CursorExpiredError) Error() string { return e.Message }

// RateLimitError is returned when a crawl request is throttled (HTTP 429).
type RateLimitError struct {
	Message    string
	RetryAfter int // seconds
}

func (e *RateLimitError) Error() string { return e.Message }

// APIError is returned for unexpected non-2xx status codes.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("registry API %d: %s", e.StatusCode, e.Body)
}

const (
	maxResponseBody = 10 * 1024 * 1024 // 10 MB
)

// Client talks to the AgenticAdvertising.org registry API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(cl *Client) { cl.http = c }
}

// NewClient creates a registry API client.
func NewClient(baseURL, token string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// FetchFeed polls the registry change feed. Pass an empty cursor for the
// initial fetch. Returns CursorExpiredError on 410.
func (c *Client) FetchFeed(ctx context.Context, cursor string, types []string, limit int) (*FeedResponse, error) {
	params := url.Values{}
	if cursor != "" {
		params.Set("cursor", cursor)
	}
	if len(types) > 0 {
		params.Set("types", strings.Join(types, ","))
	}
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/registry/feed?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build feed request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read feed response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var result FeedResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse feed response: %w", err)
		}
		return &result, nil

	case http.StatusGone:
		var fe FeedError
		if err := json.Unmarshal(body, &fe); err != nil {
			return nil, &CursorExpiredError{Message: string(body)}
		}
		return nil, &CursorExpiredError{Message: fe.Message}

	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

// SearchAgents queries agent inventory profiles with structured filters.
func (c *Client) SearchAgents(ctx context.Context, p SearchParams) (*SearchResponse, error) {
	params := url.Values{}
	if len(p.Channels) > 0 {
		params.Set("channels", strings.Join(p.Channels, ","))
	}
	if len(p.PropertyTypes) > 0 {
		params.Set("property_types", strings.Join(p.PropertyTypes, ","))
	}
	if len(p.Markets) > 0 {
		params.Set("markets", strings.Join(p.Markets, ","))
	}
	if len(p.Categories) > 0 {
		params.Set("categories", strings.Join(p.Categories, ","))
	}
	if len(p.Tags) > 0 {
		params.Set("tags", strings.Join(p.Tags, ","))
	}
	if len(p.DeliveryTypes) > 0 {
		params.Set("delivery_types", strings.Join(p.DeliveryTypes, ","))
	}
	if p.HasTMP != nil {
		params.Set("has_tmp", strconv.FormatBool(*p.HasTMP))
	}
	if p.MinProperties != nil {
		params.Set("min_properties", strconv.Itoa(*p.MinProperties))
	}
	if p.Cursor != "" {
		params.Set("cursor", p.Cursor)
	}
	if p.Limit > 0 {
		params.Set("limit", strconv.Itoa(p.Limit))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/registry/agents/search?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build search request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search agents: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	var result SearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}
	return &result, nil
}

// RequestCrawl triggers an async re-crawl of a publisher domain.
// Returns RateLimitError on 429.
func (c *Client) RequestCrawl(ctx context.Context, domain string) (*CrawlResponse, error) {
	reqBody, err := json.Marshal(CrawlRequest{Domain: domain})
	if err != nil {
		return nil, fmt.Errorf("marshal crawl request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/registry/crawl-request", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build crawl request: %w", err)
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request crawl: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read crawl response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusAccepted:
		var result CrawlResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse crawl response: %w", err)
		}
		return &result, nil

	case http.StatusTooManyRequests:
		var rl struct {
			Error      string `json:"error"`
			RetryAfter int    `json:"retry_after"`
		}
		if err := json.Unmarshal(body, &rl); err != nil {
			return nil, &RateLimitError{Message: string(body)}
		}
		return nil, &RateLimitError{Message: rl.Error, RetryAfter: rl.RetryAfter}

	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}
