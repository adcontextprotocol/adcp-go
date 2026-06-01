package registry

import "context"

// Store persists registry state across process restarts so an in-memory
// index can be rehydrated without re-bootstrapping the feed. All methods
// must be idempotent: Put on an existing key replaces, Remove on a
// missing key is a no-op, Clear on an empty namespace is a no-op.
//
// Store embeds CursorStore so a single backend supplies both the feed
// cursor and the materialized indexes.
//
// Implementations live under sub-packages (e.g. registry/glidestore,
// registry/redisstore) so the core registry package does not pull in
// client libraries.
type Store interface {
	CursorStore

	// PutProperty inserts or replaces the canonical property record. The
	// in-memory PropertyIndex rebuilds its domain/RID side-indexes from
	// this record on Hydrate, so only the canonical struct is persisted.
	PutProperty(ctx context.Context, p *Property) error
	RemoveProperty(ctx context.Context, propertyID string) error
	ClearProperties(ctx context.Context) error
	LoadProperties(ctx context.Context) ([]Property, error)

	// PutAuth inserts or replaces an authorization entry. The triple
	// (agent_url, publisher_domain, authorization_type) is the natural
	// key; repeated Put with the same triple replaces.
	PutAuth(ctx context.Context, e AuthorizationEntry) error
	// RemoveAuthEntry removes every authorization type for an
	// (agent_url, publisher_domain) pair.
	RemoveAuthEntry(ctx context.Context, agentURL, publisherDomain string) error
	// RemoveAuthAgent removes every authorization entry for an agent
	// across all publisher domains.
	RemoveAuthAgent(ctx context.Context, agentURL string) error
	ClearAuth(ctx context.Context) error
	LoadAuth(ctx context.Context) ([]AuthorizationEntry, error)

	PutAgent(ctx context.Context, p *AgentProfile) error
	RemoveAgent(ctx context.Context, agentURL string) error
	ClearAgents(ctx context.Context) error
	LoadAgents(ctx context.Context) ([]AgentProfile, error)
}
