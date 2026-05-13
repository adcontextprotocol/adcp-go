// Package scope3 implements an identityconfig.Source backed by a Scope3
// HTTP endpoint that returns the seller-keyed identity configs.
//
// Wire contract:
//
//	POST <configured URL>
//	Authorization: Bearer <configured token>
//	Content-Type: application/json
//
//	{ "after": "2026-05-13T10:42:43.123456789Z" }   (optional)
//
//	→ 200 OK
//	{
//	  "last_updated_at": "2026-05-13T11:00:00.000000000Z",
//	  "targeting_configs": [
//	    {
//	      "seller_agent_url": "https://seller.example.com/agent",
//	      "package_id": "pkg-1",
//	      "target_segments": { "all_of": [...], "any_of": [...], "none_of": [...] }
//	    }
//	  ],
//	  "removed_targeting_configs": [
//	    { "seller_agent_url": "...", "package_id": "..." }
//	  ]
//	}
package scope3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
)

// Source posts identity-config queries to a configurable URL with Bearer
// authentication and parses the JSON response into identityconfig types.
type Source struct {
	url    string
	token  string
	client *http.Client
}

// Option configures the Source at construction time.
type Option func(*Source)

// WithHTTPClient supplies a pre-configured *http.Client. Useful for custom
// transports, dial timeouts, or test fakes. Overrides any timeout set via
// WithHTTPTimeout.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Source) {
		s.client = client
	}
}

// WithHTTPTimeout sets the total request timeout on the default HTTP client.
// Ignored when a custom client is supplied via WithHTTPClient.
func WithHTTPTimeout(d time.Duration) Option {
	return func(s *Source) {
		if s.client == nil || s.client == http.DefaultClient {
			s.client = &http.Client{Timeout: d}
			return
		}
		s.client.Timeout = d
	}
}

// New constructs a Source. URL and bearer token are required.
func New(url, bearerToken string, opts ...Option) (*Source, error) {
	if url == "" {
		return nil, errors.New("scope3: URL is required")
	}
	if bearerToken == "" {
		return nil, errors.New("scope3: bearer token is required")
	}
	s := &Source{url: url, token: bearerToken, client: &http.Client{Timeout: 30 * time.Second}}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// LoadAll fetches the entire current state of the world.
func (s *Source) LoadAll(ctx context.Context) (identityconfig.Snapshot, error) {
	body, err := s.post(ctx, requestBody{})
	if err != nil {
		return identityconfig.Snapshot{}, err
	}
	return identityconfig.Snapshot{
		Configs:       toEntries(body.TargetingConfigs),
		LastUpdatedAt: body.LastUpdatedAt,
	}, nil
}

// LoadUpdatedAfter fetches the configs changed since `after`.
func (s *Source) LoadUpdatedAfter(ctx context.Context, after time.Time) (identityconfig.Delta, error) {
	body, err := s.post(ctx, requestBody{After: &after})
	if err != nil {
		return identityconfig.Delta{}, err
	}
	return identityconfig.Delta{
		Upserted:      toEntries(body.TargetingConfigs),
		Removed:       toKeys(body.RemovedTargetingConfigs),
		LastUpdatedAt: body.LastUpdatedAt,
	}, nil
}

type requestBody struct {
	After *time.Time `json:"after,omitempty"`
}

type responseBody struct {
	LastUpdatedAt           time.Time          `json:"last_updated_at"`
	TargetingConfigs        []wireConfig       `json:"targeting_configs"`
	RemovedTargetingConfigs []wireRemovedEntry `json:"removed_targeting_configs"`
}

type wireConfig struct {
	SellerAgentURL string                 `json:"seller_agent_url"`
	PackageID      string                 `json:"package_id"`
	TargetSegments *targeting.SegmentRule `json:"target_segments,omitempty"`
}

type wireRemovedEntry struct {
	SellerAgentURL string `json:"seller_agent_url"`
	PackageID      string `json:"package_id"`
}

func (s *Source) post(ctx context.Context, body requestBody) (*responseBody, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("scope3: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("scope3: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scope3: POST %s: %w", s.url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("scope3: POST %s returned %d: %s", s.url, resp.StatusCode, string(snippet))
	}

	var out responseBody
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("scope3: decode response: %w", err)
	}
	return &out, nil
}

func toEntries(cfgs []wireConfig) []identityconfig.Entry {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]identityconfig.Entry, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, identityconfig.Entry{
			Key:            identityconfig.Key{SellerAgentURL: c.SellerAgentURL, PackageID: c.PackageID},
			TargetSegments: c.TargetSegments,
		})
	}
	return out
}

func toKeys(items []wireRemovedEntry) []identityconfig.Key {
	if len(items) == 0 {
		return nil
	}
	out := make([]identityconfig.Key, 0, len(items))
	for _, it := range items {
		out = append(out, identityconfig.Key{SellerAgentURL: it.SellerAgentURL, PackageID: it.PackageID})
	}
	return out
}
