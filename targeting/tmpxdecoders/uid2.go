package tmpxdecoders

import (
	"context"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/targeting/internal/uid2"
)

// UID2Client is the subset of the UID2 operator Client the UID2 decoder
// depends on. Declared here as a local interface so unit tests can
// supply a fake without spinning up an httptest server and without
// consumers taking a transitive dependency on the internal package.
//
// The same interface shape backs the EUID decoder — EUID is a UID2
// fork with EU-jurisdiction operators, identical wire semantics and
// identical raw-ID shape. The two decoders exist as sibling types to
// keep the type registration (tmproto.UIDTypeUID2 vs
// tmproto.UIDTypeEUID) unambiguous in the registry.
type UID2Client interface {
	Decrypt(ctx context.Context, token string) ([]byte, error)
}

// ErrUID2NoMapping is the canonical "operator reachable but no
// mapping" sentinel — same value as uid2.ErrNoMapping, re-exported for
// callers that match on it via errors.Is without taking a direct
// dependency on the uid2 package.
var ErrUID2NoMapping = uid2.ErrNoMapping

// UID2 decodes a UID2 encrypted advertising token into the 32-byte
// raw UID2 (the pre-hex form of the SHA-256 of the normalized email
// or phone). The operator client is the source of truth for the
// bytes that flow downstream — the decoder passes the returned slice
// through verbatim and lets selectEntries enforce the per-type
// length.
//
// Miss reasons (opted-out, expired, not decryptable with the
// configured operator keys) all collapse to ErrDropFromSeal here,
// mirroring the LiveRamp decoder pattern. Operators debug the
// specific reason via the counter (`decoder_drop`) and adapter-side
// logs rather than by re-plumbing a distinct sentinel per reason —
// keeping the drop path uniform lets the identity-agent metrics
// treat all identity-format receivers the same way.
type UID2 struct {
	Client UID2Client
}

// Decode calls the operator via Client and returns the raw UID2 bytes.
// ErrUID2NoMapping is rewritten as ErrDropFromSeal so a miss drops
// the identity silently.
func (u UID2) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return uid2Decrypt(ctx, u.Client, userToken, "uid2")
}

// EUID decodes an EUID encrypted advertising token via an EU-jurisdiction
// operator. Behaves identically to UID2 — same wire shape, same
// 32-byte raw ID, same miss semantics. Sibling type so the registry
// pairs (tmproto.UIDTypeEUID) → EUID{} unambiguously and callers can
// wire distinct operator endpoints / credentials for the two scopes.
type EUID struct {
	Client UID2Client
}

// Decode calls the operator via Client and returns the raw EUID bytes.
// ErrUID2NoMapping is rewritten as ErrDropFromSeal so a miss drops
// the identity silently.
func (e EUID) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return uid2Decrypt(ctx, e.Client, userToken, "euid")
}

func uid2Decrypt(ctx context.Context, client UID2Client, token, label string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%s: operator client is not configured", label)
	}
	raw, err := client.Decrypt(ctx, token)
	if err != nil {
		if errors.Is(err, ErrUID2NoMapping) {
			return nil, ErrDropFromSeal
		}
		return nil, fmt.Errorf("%s: operator decrypt: %w", label, err)
	}
	if len(raw) == 0 {
		return nil, ErrDropFromSeal
	}
	return raw, nil
}
