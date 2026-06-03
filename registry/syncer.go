package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// SyncerConfig configures the feed polling loop.
type SyncerConfig struct {
	PollInterval   time.Duration // steady-state interval (default 30s)
	MaxBackoff     time.Duration // max backoff on errors (default 60s)
	FeedLimit      int           // events per page during steady-state (default 1000)
	BootstrapLimit int           // events per page during initial catchup (default 10000)
	EventTypes     []string      // optional type filter globs
}

func (c *SyncerConfig) defaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 30 * time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 60 * time.Second
	}
	if c.FeedLimit == 0 {
		c.FeedLimit = 1000
	}
	if c.BootstrapLimit == 0 {
		c.BootstrapLimit = 10000
	}
}

// Syncer polls the registry feed and applies events to local indexes.
type Syncer struct {
	client     *Client
	properties *PropertyIndex
	auth       *AuthIndex
	agents     *AgentIndex
	cursor     CursorStore
	config     SyncerConfig
	log        *slog.Logger
}

// NewSyncer creates a syncer that keeps the given indexes up to date.
func NewSyncer(client *Client, properties *PropertyIndex, auth *AuthIndex, agents *AgentIndex, cursor CursorStore, config SyncerConfig) *Syncer {
	config.defaults()
	return &Syncer{
		client:     client,
		properties: properties,
		auth:       auth,
		agents:     agents,
		cursor:     cursor,
		config:     config,
		log:        slog.Default().With("component", "registry-sync"),
	}
}

// Run starts the sync loop. It blocks until ctx is cancelled.
//
// Each index with an attached Store is hydrated before the feed loop
// begins so a saved cursor resumes against populated memory rather than
// empty maps. If any hydrate fails AND a non-empty cursor was loaded,
// the cursor is discarded and a bootstrap is forced — resuming past
// events with an empty index would silently lose every entity created
// before the cursor.
//
// When dual-write persistence is enabled, the cursor only advances
// after every event in a page successfully persisted. A persist failure
// keeps the old cursor, so the next FetchFeed re-delivers the same
// events; Put/Remove on both indexes and the Store are idempotent so
// re-delivery is safe.
func (s *Syncer) Run(ctx context.Context) error {
	hydrateFailed := false
	if err := s.properties.Hydrate(ctx); err != nil {
		s.log.Error("property index hydrate failed", "error", err)
		hydrateFailed = true
	}
	if err := s.auth.Hydrate(ctx); err != nil {
		s.log.Error("auth index hydrate failed", "error", err)
		hydrateFailed = true
	}
	if err := s.agents.Hydrate(ctx); err != nil {
		s.log.Error("agent index hydrate failed", "error", err)
		hydrateFailed = true
	}

	cursorVal, err := s.cursor.Load(ctx)
	if err != nil {
		return err
	}
	if hydrateFailed && cursorVal != "" {
		s.log.Warn("hydrate failed but cursor was set; forcing bootstrap to avoid resuming past events with empty memory",
			"cursor", cursorVal)
		cursorVal = ""
	}

	backoff := time.Second
	bootstrapping := cursorVal == ""
	var saveFailures int

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		limit := s.config.FeedLimit
		if bootstrapping {
			limit = s.config.BootstrapLimit
		}

		resp, err := s.client.FetchFeed(ctx, cursorVal, s.config.EventTypes, limit)
		if err != nil {
			var expired *CursorExpiredError
			if errors.As(err, &expired) {
				s.log.Warn("cursor expired, clearing indexes and re-bootstrapping", "cursor", cursorVal)
				if !s.clearAllForRebootstrap(ctx) {
					// Persistent Clear failed for at least one index.
					// Memory is empty (so no stale reads), but the
					// persistent backend still holds stale state; do not
					// wipe the cursor or we lose pre-expiry events
					// permanently. Retry next iteration.
					select {
					case <-time.After(backoff):
					case <-ctx.Done():
						return ctx.Err()
					}
					backoff = min(backoff*2, s.config.MaxBackoff)
					continue
				}
				cursorVal = ""
				if err := s.cursor.Save(ctx, ""); err != nil {
					s.log.Error("failed to clear persisted cursor", "error", err)
				}
				bootstrapping = true
				continue
			}

			if ctx.Err() != nil {
				return ctx.Err()
			}

			s.log.Error("feed poll failed", "error", err, "backoff", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff = min(backoff*2, s.config.MaxBackoff)
			continue
		}

		persistFailed := false
		for _, event := range resp.Events {
			if err := s.applyEvent(ctx, event); err != nil {
				var invalidDomain *InvalidDomainError
				if errors.As(err, &invalidDomain) {
					// Permanent error: same input will fail forever.
					// Drop the event and let the cursor advance.
					s.log.Warn("dropping event with permanent error",
						"event_id", event.EventID, "event_type", event.EventType, "error", err)
					continue
				}
				s.log.Error("event persist failed; cursor will not advance until retry",
					"event_id", event.EventID, "event_type", event.EventType, "error", err)
				persistFailed = true
			}
		}

		if persistFailed {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			backoff = min(backoff*2, s.config.MaxBackoff)
			continue
		}

		if resp.Cursor != nil {
			cursorVal = *resp.Cursor
			if err := s.cursor.Save(ctx, cursorVal); err != nil {
				saveFailures++
				s.log.Error("failed to save cursor", "error", err, "consecutive_failures", saveFailures)
				if saveFailures >= 3 {
					return fmt.Errorf("cursor persistence failed %d consecutive times: %w", saveFailures, err)
				}
			} else {
				saveFailures = 0
			}
		}

		backoff = time.Second

		if resp.HasMore {
			continue
		}

		bootstrapping = false

		select {
		case <-time.After(s.config.PollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// clearAllForRebootstrap clears all three indexes and reports whether
// the persistent side also cleared cleanly. Memory is wiped regardless;
// a false return signals that the persistent backend still holds stale
// data and the caller must not advance the cursor.
func (s *Syncer) clearAllForRebootstrap(ctx context.Context) bool {
	ok := true
	if err := s.properties.Clear(ctx); err != nil {
		s.log.Error("ClearProperties failed during cursor-expired handling", "error", err)
		ok = false
	}
	if err := s.auth.Clear(ctx); err != nil {
		s.log.Error("ClearAuth failed during cursor-expired handling", "error", err)
		ok = false
	}
	if err := s.agents.Clear(ctx); err != nil {
		s.log.Error("ClearAgents failed during cursor-expired handling", "error", err)
		ok = false
	}
	return ok
}

func (s *Syncer) applyEvent(ctx context.Context, event FeedEvent) error {
	switch event.EventType {
	case "property.created", "property.updated":
		var p Property
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			s.log.Warn("bad property event payload", "event_id", event.EventID, "error", err)
			return nil
		}
		if p.PropertyID == "" || p.PropertyRID == 0 {
			s.log.Warn("property event missing required fields", "event_id", event.EventID,
				"property_id", p.PropertyID, "property_rid", p.PropertyRID)
			return nil
		}
		return s.properties.Put(ctx, &p)

	case "property.removed":
		return s.properties.Remove(ctx, event.EntityID)

	case "property.merged":
		// Merge: the alias property (EntityID) is removed; the canonical remains.
		return s.properties.Remove(ctx, event.EntityID)

	case "authorization.granted":
		var entry AuthorizationEntry
		if err := json.Unmarshal(event.Payload, &entry); err != nil {
			s.log.Warn("bad authorization event payload", "event_id", event.EventID, "error", err)
			return nil
		}
		if entry.AgentURL == "" || entry.PublisherDomain == "" {
			s.log.Warn("authorization event missing required fields", "event_id", event.EventID)
			return nil
		}
		if err := ValidatePublisherDomain(entry.PublisherDomain); err != nil {
			// Permanent validation failure: dropping is the only progress
			// path. Returning the error would block the cursor and the
			// same event would re-deliver forever.
			s.log.Warn("dropping authorization event with invalid publisher_domain",
				"event_id", event.EventID, "publisher_domain", entry.PublisherDomain, "error", err)
			return nil
		}
		return s.auth.Add(ctx, entry)

	case "authorization.revoked":
		var revoke struct {
			AgentURL        string `json:"agent_url"`
			PublisherDomain string `json:"publisher_domain"`
		}
		if err := json.Unmarshal(event.Payload, &revoke); err != nil {
			s.log.Warn("bad revocation event payload", "event_id", event.EventID, "error", err)
			return nil
		}
		if revoke.AgentURL == "" || revoke.PublisherDomain == "" {
			s.log.Warn("revocation event missing required fields", "event_id", event.EventID)
			return nil
		}
		if err := ValidatePublisherDomain(revoke.PublisherDomain); err != nil {
			s.log.Warn("dropping revocation event with invalid publisher_domain",
				"event_id", event.EventID, "publisher_domain", revoke.PublisherDomain, "error", err)
			return nil
		}
		return s.auth.RemoveEntry(ctx, revoke.AgentURL, revoke.PublisherDomain)

	case "agent.discovered", "agent.updated":
		var p AgentProfile
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			s.log.Warn("bad agent event payload", "event_id", event.EventID, "error", err)
			return nil
		}
		if p.AgentURL == "" {
			s.log.Warn("agent event missing agent_url", "event_id", event.EventID)
			return nil
		}
		return s.agents.Put(ctx, &p)

	case "agent.removed":
		agentURL := event.EntityID
		if err := s.agents.Remove(ctx, agentURL); err != nil {
			return err
		}
		return s.auth.RemoveAgent(ctx, agentURL)

	default:
		s.log.Debug("unhandled event type", "type", event.EventType, "event_id", event.EventID)
		return nil
	}
}
