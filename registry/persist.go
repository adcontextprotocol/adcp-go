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
//
// Trade-off: this is a single fat interface rather than per-entity
// Persisters (PropertyPersister, AuthPersister, AgentPersister). A
// single interface keeps construction simple at the cost of forcing
// backends to implement every entity even when only one is needed. If
// mix-and-match backends ever become a real requirement (e.g. Postgres
// for auth, Valkey for properties), splitting is a mechanical refactor.
//
// Authentication, TLS, retries, and timeouts live in the client passed
// to the concrete backend's constructor; Store implementations should
// not introduce a second config surface.
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
	// key; repeated Put with the same triple replaces. Implementations
	// must reject publisher_domain values containing '|' or whitespace
	// since both are reserved by the on-wire field-name encoding.
	PutAuth(ctx context.Context, e AuthorizationEntry) error
	// RemoveAuthEntry removes every authorization type for an
	// (agent_url, publisher_domain) pair. The registry feed's
	// authorization.revoked event carries no authorization_type, so
	// per-type removal would have no caller; revisit if upstream changes.
	RemoveAuthEntry(ctx context.Context, agentURL, publisherDomain string) error
	// RemoveAuthAgent removes every authorization entry for an agent
	// across all publisher domains.
	RemoveAuthAgent(ctx context.Context, agentURL string) error
	// ClearAuth wipes the entire auth namespace. Implementations may use
	// SCAN+DEL which is not atomic; callers must ensure no second
	// process applies feed events concurrently with Clear.
	ClearAuth(ctx context.Context) error
	LoadAuth(ctx context.Context) ([]AuthorizationEntry, error)

	PutAgent(ctx context.Context, p *AgentProfile) error
	RemoveAgent(ctx context.Context, agentURL string) error
	ClearAgents(ctx context.Context) error
	LoadAgents(ctx context.Context) ([]AgentProfile, error)
}

// ValidatePublisherDomain rejects values that would break the
// {publisher_domain}|{authorization_type} field-name encoding used by
// every Store backend. Backends must call this from PutAuth and
// RemoveAuthEntry before touching the wire.
func ValidatePublisherDomain(publisherDomain string) error {
	if publisherDomain == "" {
		return &InvalidDomainError{Domain: publisherDomain, Reason: "empty"}
	}
	for i := 0; i < len(publisherDomain); i++ {
		c := publisherDomain[i]
		if c == '|' {
			return &InvalidDomainError{Domain: publisherDomain, Reason: "contains '|'"}
		}
		if c <= 0x20 || c == 0x7f {
			return &InvalidDomainError{Domain: publisherDomain, Reason: "contains control or whitespace byte"}
		}
	}
	return nil
}

// InvalidDomainError is returned when a publisher_domain fails encoding
// validation. The feed should never produce one of these; if it does,
// the event is dropped from the persistent path so a misbehaving
// upstream can't poison the keyspace.
type InvalidDomainError struct {
	Domain string
	Reason string
}

func (e *InvalidDomainError) Error() string {
	return "registry: invalid publisher_domain " + e.Domain + ": " + e.Reason
}
