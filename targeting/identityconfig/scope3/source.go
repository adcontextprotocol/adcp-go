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
//	  "lastUpdatedAt": "2026-05-13T11:00:00.000000000Z",
//	  "targetingConfigs": [
//	    {
//	      "sellerAgentUrl": "https://seller.example.com/agent",
//	      "packageId": "pkg-1",
//	      "targetSegments": { "allOf": [...], "anyOf": [...], "noneOf": [...] }
//	    }
//	  ],
//	  "removedTargetingConfigs": [
//	    { "sellerAgentUrl": "...", "packageId": "..." }
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
	url            string
	token          string
	client         *http.Client
	customClient   bool // true when client was set via WithHTTPClient
}

// Option configures the Source at construction time.
type Option func(*Source)

// WithHTTPClient supplies a pre-configured *http.Client. Useful for custom
// transports, dial timeouts, or test fakes. Suppresses WithHTTPTimeout — the
// caller's client owns its own timeout.
func WithHTTPClient(client *http.Client) Option {
	return func(s *Source) {
		s.client = client
		s.customClient = true
	}
}

// WithHTTPTimeout sets the total request timeout on the Source's own HTTP
// client. Has no effect when WithHTTPClient was used — that path treats the
// caller's client as authoritative, so its Timeout is not mutated regardless
// of option order.
func WithHTTPTimeout(d time.Duration) Option {
	return func(s *Source) {
		if s.customClient {
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
	LastUpdatedAt           time.Time          `json:"lastUpdatedAt"`
	TargetingConfigs        []wireConfig       `json:"targetingConfigs"`
	RemovedTargetingConfigs []wireRemovedEntry `json:"removedTargetingConfigs"`
}

type wireConfig struct {
	SellerAgentURL string            `json:"sellerAgentUrl"`
	PackageID      string            `json:"packageId"`
	TargetSegments *wireSegmentRule  `json:"targetSegments,omitempty"`
}

type wireRemovedEntry struct {
	SellerAgentURL string `json:"sellerAgentUrl"`
	PackageID      string `json:"packageId"`
}

type wireSegmentRule struct {
	AllOf  []string `json:"allOf,omitempty"`
	AnyOf  []string `json:"anyOf,omitempty"`
	NoneOf []string `json:"noneOf,omitempty"`
}

// toDomain projects the wire rule onto its domain twin. Slice fields alias
// the wire struct rather than deep-copying; this is safe because the wire
// struct is request-scoped and identityconfig.Service.GetBySeller clones the
// rule before exposing it to readers, so external callers never observe the
// alias. Mutating a returned *SegmentRule before that clone would corrupt
// the snapshot.
func (w *wireSegmentRule) toDomain() *targeting.SegmentRule {
	if w == nil {
		return nil
	}
	return &targeting.SegmentRule{
		AllOf:  w.AllOf,
		AnyOf:  w.AnyOf,
		NoneOf: w.NoneOf,
	}
}

func (s *Source) post(ctx context.Context, body requestBody) (out *responseBody, retErr error) {
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
	defer func() {
		// Drain any unread bytes so the underlying connection can be
		// returned to the pool, then close. Close errors are usually
		// downstream effects of whatever produced retErr on the read
		// path — but on the rare path where the read succeeded and
		// Close still fails (or both fail for independent reasons),
		// joining preserves both for diagnostics.
		_, _ = io.Copy(io.Discard, resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("scope3: close response body: %w", closeErr))
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
		if readErr != nil {
			return nil, fmt.Errorf("scope3: POST %s returned %d (body read failed: %v)", s.url, resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("scope3: POST %s returned %d: %s", s.url, resp.StatusCode, string(snippet))
	}

	var decoded responseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("scope3: decode response: %w", err)
	}
	return &decoded, nil
}

func toEntries(cfgs []wireConfig) []identityconfig.Entry {
	if len(cfgs) == 0 {
		return nil
	}
	out := make([]identityconfig.Entry, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, identityconfig.Entry{
			Key:            identityconfig.Key{SellerAgentURL: c.SellerAgentURL, PackageID: c.PackageID},
			TargetSegments: c.TargetSegments.toDomain(),
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
