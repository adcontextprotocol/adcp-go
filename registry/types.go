// Package registry provides a sync client for the AgenticAdvertising.org
// registry. It polls the cursor-based event feed and maintains local indexes
// for property resolution (property_id ↔ property_rid) and authorization checks.
package registry

import (
	"encoding/json"
	"time"
)

// FeedEvent is a single event from the registry change feed.
type FeedEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	Actor      string          `json:"actor"`
	CreatedAt  time.Time       `json:"created_at"`
}

// FeedResponse is the response from GET /api/registry/feed.
type FeedResponse struct {
	Events  []FeedEvent `json:"events"`
	Cursor  *string     `json:"cursor"`
	HasMore bool        `json:"has_more"`
}

// FeedError is the 410 response when a cursor has expired.
type FeedError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// AgentProfile is an agent inventory profile from the search endpoint.
type AgentProfile struct {
	AgentURL         string          `json:"agent_url"`
	Channels         []string        `json:"channels"`
	PropertyTypes    []string        `json:"property_types"`
	Markets          []string        `json:"markets"`
	Categories       []string        `json:"categories"`
	Tags             []string        `json:"tags"`
	DeliveryTypes    []string        `json:"delivery_types"`
	FormatIDs        json.RawMessage `json:"format_ids"`
	PropertyCount    int             `json:"property_count"`
	PublisherCount   int             `json:"publisher_count"`
	HasTMP           bool            `json:"has_tmp"`
	CategoryTaxonomy *string         `json:"category_taxonomy"`
	RelevanceScore   float64         `json:"relevance_score"`
	MatchedFilters   []string        `json:"matched_filters"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// SearchResponse is the response from GET /api/registry/agents/search.
type SearchResponse struct {
	Results []AgentProfile `json:"results"`
	Cursor  *string        `json:"cursor"`
	HasMore bool           `json:"has_more"`
}

// SearchParams defines filter parameters for agent search.
type SearchParams struct {
	Channels      []string
	PropertyTypes []string
	Markets       []string
	Categories    []string
	Tags          []string
	DeliveryTypes []string
	HasTMP        *bool
	MinProperties *int
	Cursor        string
	Limit         int
}

// CrawlRequest is the body for POST /api/registry/crawl-request.
type CrawlRequest struct {
	Domain string `json:"domain"`
}

// CrawlResponse is the 202 response from a crawl request.
type CrawlResponse struct {
	Message string `json:"message"`
	Domain  string `json:"domain"`
}
