package identity

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// BindOptions qualifies a binding.
type BindOptions struct {
	// ExistingRFC9421Principal marks principalID as a principal that already
	// authenticates with the RFC 9421 profile. Binding onto it needs
	// MigrationApproved: controlling the agent's host was not enough to
	// obtain its RFC 9421 keys, so origin control alone must not inherit
	// that principal.
	ExistingRFC9421Principal bool
	// MigrationApproved records the seller's explicit, out-of-band approval
	// to bind onto an existing RFC 9421 principal. Never set automatically.
	MigrationApproved bool
	// Role is the trust-record role the origin must carry. Defaults to
	// adcp:buying.
	Role string
}

// BindingStore records origin-to-principal bindings: the seller's explicit,
// audited decisions. An origin maps to at most one principal; a principal
// may own several origins. Implementations are application-owned and
// durable in production. Discovery and verification never write bindings.
type BindingStore interface {
	signing.OriginBindingLookup
	Bind(ctx context.Context, origin, principalID string, opts BindOptions) error
}

// ErrRebind is returned when an origin is already bound to a different
// principal.
var ErrRebind = errors.New("identity: an origin cannot be rebound to another principal")

// ErrMigrationNotApproved is returned when binding onto an existing RFC 9421
// principal without explicit approval.
var ErrMigrationNotApproved = errors.New("identity: binding an existing RFC 9421 principal requires explicit approval")

// OriginBindings is an in-memory BindingStore for one process, for tests and
// examples. Production deployments supply a durable store. Bindings persist
// when the operator later delists an origin: the BindingGate then denies
// authority, but the historical binding still refuses an RFC 9421
// downgrade.
type OriginBindings struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewOriginBindings returns an empty OriginBindings.
func NewOriginBindings() *OriginBindings {
	return &OriginBindings{m: map[string]string{}}
}

// Bind implements BindingStore.
func (b *OriginBindings) Bind(_ context.Context, origin, principalID string, opts BindOptions) error {
	canonical, err := signing.CanonicalOrigin(origin)
	if err != nil || !strings.HasPrefix(canonical, "https://") || principalID == "" {
		return errors.New("identity: binding requires an HTTPS origin and a principal ID")
	}
	if opts.ExistingRFC9421Principal && !opts.MigrationApproved {
		return ErrMigrationNotApproved
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.m[canonical]; ok && existing != principalID {
		return ErrRebind
	}
	b.m[canonical] = principalID
	return nil
}

// PrincipalForOrigin implements signing.OriginBindingLookup.
func (b *OriginBindings) PrincipalForOrigin(_ context.Context, origin string) (string, bool, error) {
	canonical, err := signing.CanonicalOrigin(origin)
	if err != nil {
		return "", false, nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	p, ok := b.m[canonical]
	return p, ok, nil
}

// BindingGate is the signing.WBAAuthorizer that turns a verified origin
// into a principal. An unbound origin is identity-established (it returns
// ""; the middleware then serves it as an anonymous caller beyond
// UnboundFor). A bound origin needs a revocation list and a current
// listing:
//   - its key's revocation state (VerifiedSigner.Revocation, from the
//     middleware's revocation check): an origin that serves no revocation
//     list, or one no checker ran for, is identity-established and cannot
//     act for its principal; while its list is stale (unreadable, or past
//     next_update plus grace), a spend-committing operation fails with the
//     retryable SERVICE_UNAVAILABLE, and while the revocation store cannot
//     be read every operation does;
//   - its operator's trust record, re-evaluated on every request through
//     the evaluator's cache, which is bounded by the revocation polling
//     interval. Listed with the role: the bound principal. Delisted (the
//     last record read does not list it, or the operator never published
//     one): identity-established, as at first contact, with no exception.
//     The binding itself is kept, so the origin still cannot fall back to
//     the RFC 9421 profile, and TrustRecordEvaluator.Bind refuses to bind it
//     again while it stays unlisted. Record unreadable (a fetch failure,
//     including a 404 after the operator published a record, is never a
//     delisting, and an outage never restores access): a spend-committing
//     operation fails with SERVICE_UNAVAILABLE, and any other operation
//     continues under the binding only while the last record that listed
//     the origin is within the evaluator's OutageContinuation bound (seller
//     policy, 4x the polling interval by default), then also gets
//     SERVICE_UNAVAILABLE.
type BindingGate struct {
	Bindings signing.OriginBindingLookup
	Records  *TrustRecordEvaluator
	// Role defaults to adcp:buying. Configure the role your receiving
	// surface needs.
	Role string
	// SpendCommitting classifies operations for the outage rules. Defaults
	// to signing.IsSpendCommittingOperation.
	SpendCommitting func(operation string) bool
}

// AuthorizeWBA implements signing.WBAAuthorizer.
func (g *BindingGate) AuthorizeWBA(ctx context.Context, signer *signing.VerifiedSigner, operation string) (string, error) {
	if g.Bindings == nil {
		return "", nil
	}
	principal, bound, err := g.Bindings.PrincipalForOrigin(ctx, signer.AgentURL)
	if err != nil {
		return "", signing.NewAuthorizationError(signing.CodeServiceUnavailable, "binding lookup failed", err)
	}
	if !bound {
		return "", nil
	}
	spend := signing.IsSpendCommittingOperation(operation)
	if g.SpendCommitting != nil {
		spend = g.SpendCommitting(operation)
	}
	switch signer.Revocation {
	case signing.RevocationCurrent:
	case signing.RevocationStale:
		if spend {
			return "", signing.NewAuthorizationError(signing.CodeServiceUnavailable, "origin revocation list is stale", nil)
		}
	case signing.RevocationUnavailable:
		return "", signing.NewAuthorizationError(signing.CodeServiceUnavailable, "revocation store unavailable", nil)
	default:
		// No revocation list (or no check): identity-established, never
		// bound.
		return "", nil
	}
	if g.Records == nil {
		return "", signing.NewAuthorizationError(signing.CodeServiceUnavailable, "binding evaluation unavailable", nil)
	}
	role := g.Role
	if role == "" {
		role = RoleBuying
	}
	l := g.Records.CheckListing(ctx, signer.AgentURL, role)
	switch l.Status {
	case Listed:
		return principal, nil
	case Unlisted:
		// Delisted or never published: identity-established again. A
		// stale delisting stays a delisting through an outage.
		return "", nil
	default:
		if spend || !l.WithinGrace {
			return "", signing.NewAuthorizationError(signing.CodeServiceUnavailable, "operator trust record unavailable", l.Err)
		}
		return principal, nil
	}
}
