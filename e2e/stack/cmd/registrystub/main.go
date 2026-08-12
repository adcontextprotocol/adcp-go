// Command registrystub serves the AdCP registry data endpoints the router
// and the context-agent consume, with no dependency on the live registry.
//
// Endpoints:
//
//	GET /api/registry/feed            cursor-paged change feed (bearer auth)
//	GET /api/registry/authorizations  per-agent authorization rows carrying
//	                                  signing_keys[] — the key source an agent
//	                                  uses under TMP_REGISTRY_MODE=authorization
//	GET /healthz                      liveness for the compose health check
//
// The feed is a fixed three-property catalog plus the authorization and agent
// events a real feed interleaves, so the syncer's event dispatch is exercised
// rather than just its property path. Cursor semantics:
//
//	no cursor      → every event, has_more=false
//	current cursor → zero events, same cursor (the steady-state page that keeps
//	                 the consumer's liveness beacon firing)
//	other cursor   → 410 cursor_expired, which drives a consumer re-bootstrap
//
// Payload shape note: each property event carries the spec's
// property.created payload fields (property_rid, classification, source,
// identifiers[], publisher_domain) AND the flat property_id / property_type /
// domain / placements that this repo's registry.Property decoder requires.
// The spec's payload objects allow additional properties, so the superset is
// valid on the wire and consumable by both readers.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// finalCursor is the cursor a consumer holds once it has drained the feed.
// UUID-shaped because the spec types the cursor as a uuid.
const finalCursor = "019700ff-0e2e-7000-8000-00000000f1a1"

// retentionDays mirrors the feed's documented 90-day cursor retention, which
// consumers surface in operational dashboards.
const retentionDays = 90

type feedEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	Actor      string          `json:"actor"`
	CreatedAt  time.Time       `json:"created_at"`
}

type feedFreshness struct {
	GeneratedAt          time.Time `json:"generated_at"`
	LatestEventCreatedAt time.Time `json:"latest_event_created_at"`
	LagSeconds           int       `json:"lag_seconds"`
	RetentionDays        int       `json:"retention_days"`
}

type feedResponse struct {
	Events    []feedEvent   `json:"events"`
	Cursor    *string       `json:"cursor"`
	HasMore   bool          `json:"has_more"`
	Freshness feedFreshness `json:"freshness"`
}

type authorizationRow struct {
	ID              string               `json:"id"`
	AgentURL        string               `json:"agent_url"`
	PropertyRID     string               `json:"property_rid"`
	PropertyIDSlug  string               `json:"property_id_slug"`
	PublisherDomain string               `json:"publisher_domain"`
	AuthorizedFor   string               `json:"authorized_for"`
	Evidence        string               `json:"evidence"`
	SigningKeys     []tmproto.SigningKey `json:"signing_keys"`
}

type authorizationsResponse struct {
	Rows []authorizationRow `json:"rows"`
}

func main() {
	port := flag.Int("port", fixture.RegistryStubPort, "listen port")
	sharedDir := flag.String("shared", "/shared", "directory holding the router's public signing JWK")
	jwkWait := flag.Duration("jwk-wait", 60*time.Second, "how long to wait for the router JWK to appear")
	flag.Parse()

	// The authorization rows publish the router's public key, so the stub
	// cannot serve them until bootstrap has generated it. Waiting here (as
	// opposed to failing) lets compose start the stub and the bootstrap
	// one-shot in either order.
	jwkPath := filepath.Join(*sharedDir, "router-signing-key.jwk")
	routerKey, err := waitForJWK(jwkPath, *jwkWait)
	if err != nil {
		log.Fatalf("registrystub: %v", err)
	}
	log.Printf("registrystub: loaded router signing key kid=%s", routerKey.Kid)

	events := buildEvents()
	var feedPolls, authLookups atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/registry/feed", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		feedPolls.Add(1)
		serveFeed(w, r, events)
	})
	mux.HandleFunc("GET /api/registry/authorizations", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		authLookups.Add(1)
		serveAuthorizations(w, r, routerKey)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Call counters, so a failing run can distinguish "nobody polled the
	// feed" from "the feed served the wrong thing".
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]int64{
			"feed_polls":            feedPolls.Load(),
			"authorization_lookups": authLookups.Load(),
		})
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("registrystub: listening on %s (%d events, %d properties)",
		addr, len(events), len(fixture.Properties))
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("registrystub: serve: %v", err)
	}
}

func authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") == "Bearer "+fixture.RegistryFeedToken {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{
		"error":   "unauthorized",
		"message": "bearer token required",
	})
	return false
}

// serveFeed applies the cursor and `types` filter. `types` accepts the
// comma-separated globs the real feed does, so a consumer narrowing to
// property.* gets only property events.
func serveFeed(w http.ResponseWriter, r *http.Request, all []feedEvent) {
	cursor := r.URL.Query().Get("cursor")
	switch cursor {
	case "":
		// Full drain below.
	case finalCursor:
		// Caught up. A zero-event page still advances the consumer's
		// liveness beacon, which is what a quiescent real feed does.
		writeFeed(w, nil)
		return
	default:
		writeJSON(w, http.StatusGone, map[string]string{
			"error":   "cursor_expired",
			"message": fmt.Sprintf("cursor %q is outside the %d-day retention window", cursor, retentionDays),
		})
		return
	}

	selected := filterByTypes(all, r.URL.Query().Get("types"))
	if limit := r.URL.Query().Get("limit"); limit != "" {
		// The stub's whole catalog fits in one page, so `limit` only has to
		// be accepted, not honored as pagination. Rejecting a malformed
		// value keeps the contract honest.
		if _, err := fmt.Sscanf(limit, "%d", new(int)); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":   "invalid_limit",
				"message": fmt.Sprintf("limit %q is not an integer", limit),
			})
			return
		}
	}
	writeFeed(w, selected)
}

func writeFeed(w http.ResponseWriter, events []feedEvent) {
	now := time.Now().UTC()
	latest := now
	if len(events) > 0 {
		latest = events[len(events)-1].CreatedAt
	}
	cursor := finalCursor
	writeJSON(w, http.StatusOK, feedResponse{
		Events:  events,
		Cursor:  &cursor,
		HasMore: false,
		Freshness: feedFreshness{
			GeneratedAt:          now,
			LatestEventCreatedAt: latest,
			LagSeconds:           int(now.Sub(latest).Seconds()),
			RetentionDays:        retentionDays,
		},
	})
}

func filterByTypes(all []feedEvent, types string) []feedEvent {
	types = strings.TrimSpace(types)
	if types == "" {
		return all
	}
	patterns := strings.Split(types, ",")
	out := make([]feedEvent, 0, len(all))
	for _, ev := range all {
		for _, p := range patterns {
			if matchType(strings.TrimSpace(p), ev.EventType) {
				out = append(out, ev)
				break
			}
		}
	}
	return out
}

// matchType supports the trailing-* glob the feed's `types` filter accepts
// (e.g. "property.*"), plus exact names.
func matchType(pattern, eventType string) bool {
	if pattern == "" {
		return false
	}
	if suffix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(eventType, suffix)
	}
	return pattern == eventType
}

// serveAuthorizations answers the per-agent key lookup an agent performs
// under TMP_REGISTRY_MODE=authorization. The router signs on behalf of the
// publisher, so its public key is the signing_keys entry on every row for
// the seller agent this stack runs. Any other agent_url gets a 404, which
// the consumer caches negatively.
func serveAuthorizations(w http.ResponseWriter, r *http.Request, routerKey tmproto.SigningKey) {
	agentURL := r.URL.Query().Get("agent_url")
	if agentURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "missing_agent_url",
			"message": "agent_url query parameter is required",
		})
		return
	}
	if !sameAgent(agentURL, fixture.SellerAgentURL) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":   "not_found",
			"message": fmt.Sprintf("no authorizations for %q", agentURL),
		})
		return
	}
	rows := make([]authorizationRow, 0, len(fixture.Properties))
	for i, p := range fixture.Properties {
		rows = append(rows, authorizationRow{
			ID:              fmt.Sprintf("019700ff-0e2e-7000-8000-0000000000a%d", i+1),
			AgentURL:        fixture.SellerAgentURL,
			PropertyRID:     p.PropertyRID,
			PropertyIDSlug:  p.PropertyID,
			PublisherDomain: p.Domain,
			AuthorizedFor:   p.PropertyID,
			Evidence:        "adagents_json",
			SigningKeys:     []tmproto.SigningKey{routerKey},
		})
	}
	writeJSON(w, http.StatusOK, authorizationsResponse{Rows: rows})
}

// sameAgent compares two agent URLs the way the registry does: the consumer
// canonicalizes before it asks, so a trailing-slash difference is the only
// variation the stub has to absorb.
func sameAgent(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// buildEvents assembles the feed in the order a real registry would have
// emitted it: properties first (they are the entities everything else
// references), then the publisher→agent authorizations, then the agent
// profile.
func buildEvents() []feedEvent {
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var events []feedEvent
	seq := 0
	next := func() (string, time.Time) {
		seq++
		return fmt.Sprintf("019700ff-0e2e-7000-8000-0000000001%02x", seq), base.Add(time.Duration(seq) * time.Second)
	}

	for _, p := range fixture.Properties {
		id, at := next()
		events = append(events, feedEvent{
			EventID:    id,
			EventType:  "property.created",
			EntityType: "property",
			EntityID:   p.PropertyRID,
			Actor:      "e2e-stack",
			CreatedAt:  at,
			Payload: mustJSON(map[string]any{
				// Spec payload fields.
				"property_rid":     p.PropertyRID,
				"classification":   "property",
				"source":           "authoritative",
				"publisher_domain": p.Domain,
				"identifiers": []map[string]string{
					{"type": "domain", "value": p.Domain},
				},
				// Flat fields this repo's property decoder reads.
				"property_id":   p.PropertyID,
				"property_type": string(p.PropertyType),
				"domain":        p.Domain,
				"placements":    p.Placements,
			}),
		})
	}

	for _, p := range fixture.Properties {
		id, at := next()
		events = append(events, feedEvent{
			EventID:    id,
			EventType:  "authorization.granted",
			EntityType: "authorization",
			EntityID:   id,
			Actor:      "e2e-stack",
			CreatedAt:  at,
			Payload: mustJSON(map[string]any{
				"agent_url":          fixture.SellerAgentURL,
				"publisher_domain":   p.Domain,
				"authorization_type": "property_ids",
				"authorized_for":     p.PropertyID,
				"property_ids":       []string{p.PropertyID},
				"placement_ids":      p.Placements,
				"countries":          []string{fixture.Country},
				"delegation_type":    "direct",
				"exclusive":          false,
				"evidence":           "adagents_json",
			}),
		})
	}

	id, at := next()
	events = append(events, feedEvent{
		EventID:    id,
		EventType:  "agent.discovered",
		EntityType: "agent",
		EntityID:   fixture.SellerAgentURL,
		Actor:      "e2e-stack",
		CreatedAt:  at,
		Payload: mustJSON(map[string]any{
			"agent_url":       fixture.SellerAgentURL,
			"channels":        []string{"display", "olv"},
			"property_types":  propertyTypes(),
			"markets":         []string{fixture.Country},
			"has_tmp":         true,
			"property_count":  len(fixture.Properties),
			"publisher_count": len(fixture.Properties),
			"updated_at":      at,
		}),
	})

	return events
}

func propertyTypes() []string {
	seen := make(map[string]struct{}, len(fixture.Properties))
	out := make([]string, 0, len(fixture.Properties))
	for _, p := range fixture.Properties {
		t := string(p.PropertyType)
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func waitForJWK(path string, timeout time.Duration) (tmproto.SigningKey, error) {
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path) //nolint:gosec // path is a flag, not request input
		if err == nil {
			var key tmproto.SigningKey
			if err := json.Unmarshal(data, &key); err != nil {
				return tmproto.SigningKey{}, fmt.Errorf("parse %s: %w", path, err)
			}
			if key.Kid == "" {
				return tmproto.SigningKey{}, fmt.Errorf("%s has no kid", path)
			}
			return key, nil
		}
		if time.Now().After(deadline) {
			return tmproto.SigningKey{}, fmt.Errorf("router JWK %s did not appear within %s: %w", path, timeout, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		log.Fatalf("registrystub: marshal payload: %v", err)
	}
	return data
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("registrystub: write response: %v", err)
	}
}
