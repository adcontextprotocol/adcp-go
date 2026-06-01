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
	PollInterval time.Duration // steady-state interval (default 30s)
	MaxBackoff   time.Duration // max backoff on errors (default 60s)
	FeedLimit    int           // events per page during steady-state (default 1000)
	BootstrapLimit int         // events per page during initial catchup (default 10000)
	EventTypes   []string      // optional type filter globs
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
// empty maps. Hydration failures are logged and the loop continues — a
// degraded run that re-fetches from the feed is preferable to refusing
// to start.
func (s *Syncer) Run(ctx context.Context) error {
	if err := s.properties.Hydrate(ctx); err != nil {
		s.log.Error("property index hydrate failed", "error", err)
	}
	if err := s.auth.Hydrate(ctx); err != nil {
		s.log.Error("auth index hydrate failed", "error", err)
	}
	if err := s.agents.Hydrate(ctx); err != nil {
		s.log.Error("agent index hydrate failed", "error", err)
	}

	cursorVal, err := s.cursor.Load(ctx)
	if err != nil {
		return err
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
				s.properties.Clear()
				s.auth.Clear()
				s.agents.Clear()
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

		// Apply events
		for _, event := range resp.Events {
			s.applyEvent(event)
		}

		// Advance cursor
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

		backoff = time.Second // reset on success

		if resp.HasMore {
			continue // drain immediately
		}

		bootstrapping = false

		// Steady-state: wait before next poll
		select {
		case <-time.After(s.config.PollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Syncer) applyEvent(event FeedEvent) {
	switch event.EventType {
	case "property.created", "property.updated":
		var p Property
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			s.log.Warn("bad property event payload", "event_id", event.EventID, "error", err)
			return
		}
		if p.PropertyID == "" || p.PropertyRID == 0 {
			s.log.Warn("property event missing required fields", "event_id", event.EventID,
				"property_id", p.PropertyID, "property_rid", p.PropertyRID)
			return
		}
		s.properties.Put(&p)

	case "property.removed":
		s.properties.Remove(event.EntityID)

	case "property.merged":
		// Merge: the alias property (EntityID) is removed; the canonical remains.
		s.properties.Remove(event.EntityID)

	case "authorization.granted":
		var entry AuthorizationEntry
		if err := json.Unmarshal(event.Payload, &entry); err != nil {
			s.log.Warn("bad authorization event payload", "event_id", event.EventID, "error", err)
			return
		}
		if entry.AgentURL == "" || entry.PublisherDomain == "" {
			s.log.Warn("authorization event missing required fields", "event_id", event.EventID)
			return
		}
		s.auth.Add(entry)

	case "authorization.revoked":
		var revoke struct {
			AgentURL        string `json:"agent_url"`
			PublisherDomain string `json:"publisher_domain"`
		}
		if err := json.Unmarshal(event.Payload, &revoke); err != nil {
			s.log.Warn("bad revocation event payload", "event_id", event.EventID, "error", err)
			return
		}
		if revoke.AgentURL == "" || revoke.PublisherDomain == "" {
			s.log.Warn("revocation event missing required fields", "event_id", event.EventID)
			return
		}
		s.auth.RemoveEntry(revoke.AgentURL, revoke.PublisherDomain)

	case "agent.discovered", "agent.updated":
		var p AgentProfile
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			s.log.Warn("bad agent event payload", "event_id", event.EventID, "error", err)
			return
		}
		if p.AgentURL == "" {
			s.log.Warn("agent event missing agent_url", "event_id", event.EventID)
			return
		}
		s.agents.Put(&p)

	case "agent.removed":
		agentURL := event.EntityID
		s.agents.Remove(agentURL)
		s.auth.RemoveAgent(agentURL)

	default:
		s.log.Debug("unhandled event type", "type", event.EventType, "event_id", event.EventID)
	}
}
