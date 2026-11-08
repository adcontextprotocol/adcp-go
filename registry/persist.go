package registry

import (
	"context"
	"regexp"
	"strings"
)

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
	// Implementations must validate publisher_domain with
	// ValidatePublisherDomainKey, not the strict ValidatePublisherDomain:
	// removal must reach entries written before the strict shape check
	// existed.
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

// publisherDomainPattern is the publisher_domain pattern from
// property/base-property-source.json in the AdCP schema bundle: a bare
// lowercase hostname, labels of lowercase letters, digits, and interior
// hyphens, dots between labels. It admits single-label values and
// rejects URL syntax, wildcards, ports, and trailing dots by shape.
var publisherDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// ValidatePublisherDomain is the strict check for writes (PutAuth and
// the syncer's authorization-grant path). It rejects values that would
// break the {publisher_domain}|{authorization_type} field-name encoding
// used by every Store backend, and values that are not a bare lowercase
// hostname under the publisher_domain pattern in
// property/base-property-source.json. The strict shape matters beyond
// the encoding: a publisher_domain pasted as a URL
// ("https://www.example.com/path") would otherwise be stored as a
// registry key no seller can match. Backends must call this from
// PutAuth before touching the wire.
func ValidatePublisherDomain(publisherDomain string) error {
	if err := ValidatePublisherDomainKey(publisherDomain); err != nil {
		return err
	}
	if len(publisherDomain) > 253 {
		return &InvalidDomainError{Domain: publisherDomain, Reason: "exceeds 253 characters"}
	}
	if !publisherDomainPattern.MatchString(publisherDomain) {
		return &InvalidDomainError{Domain: publisherDomain, Reason: "not a bare lowercase hostname"}
	}
	for _, label := range strings.Split(publisherDomain, ".") {
		if len(label) > 63 {
			return &InvalidDomainError{Domain: publisherDomain, Reason: "label exceeds 63 characters"}
		}
	}
	return nil
}

// ValidatePublisherDomainKey is the loose check for removals
// (RemoveAuthEntry and the syncer's revocation path): it enforces only
// the field-name encoding constraints, not the hostname shape.
// Removal must never be stricter than the loosest write that could
// have created the entry — persistent stores populated before the
// strict write check can hold values like "Example.COM" or
// "example.com.", and gating their revocation on the strict pattern
// would strand a live grant with no way to remove it. Backends must
// call this from RemoveAuthEntry before touching the wire (the
// glide/redis backends pick this up when their registry pin bumps past
// the release that introduces it).
func ValidatePublisherDomainKey(publisherDomain string) error {
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

// InvalidDomainError is returned when a publisher_domain fails
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
