package tmpxdecoders

import (
	"context"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/targeting/internal/liveramp"
)

// LiveRampClient is the subset of the LiveRamp sidecar Client the RampID
// decoders depend on. Declared here as a local interface so unit tests can
// supply a fake without spinning up an httptest server.
type LiveRampClient interface {
	MappedID(ctx context.Context, env string) (string, error)
}

// ErrLiveRampNoMapping is the canonical "sidecar reachable but no mapping"
// sentinel — same value as liveramp.ErrNoMapping, re-exported for callers
// that match on it via errors.Is without taking a direct dependency on
// the liveramp package.
var ErrLiveRampNoMapping = liveramp.ErrNoMapping

// ErrDropFromSeal signals to selectEntries that the user_token was consumed
// successfully but the resulting identity should be omitted from the TMPX
// wire (e.g. LiveRamp returned no mapping for this RampID). The
// identityagent package re-exports an alias for callers that want to
// generate this from their own decoders.
var ErrDropFromSeal = errors.New("tmpxdecoders: drop from seal")

// RampID decodes a LiveRamp env identifier into the binary form TMPX
// packs into its plaintext. The sidecar's Scope3-mapped value is used as
// the binary token directly, matching rtdp's treatment of the field — the
// sidecar is the source of truth for the bytes that flow downstream. If
// the mapped string isn't the byte length TMPX expects for RampID
// (currently 32), the agent's selectEntries surfaces that mismatch as an
// explicit error and omits TMPX from the response.
type RampID struct {
	Client LiveRampClient
}

// Decode calls the LiveRamp sidecar with userToken and returns the
// Scope3-mapped value as raw bytes. ErrLiveRampNoMapping is rewritten as
// ErrDropFromSeal so a miss drops the identity silently.
func (r RampID) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return rampIDLookup(ctx, r.Client, userToken, "rampid")
}

// RampIDDerived decodes a LiveRamp derived env. Behaves identically to
// RampID — the only difference is the per-type byte length TMPX enforces.
type RampIDDerived struct {
	Client LiveRampClient
}

// Decode calls the LiveRamp sidecar with userToken and returns the
// Scope3-mapped value as raw bytes.
func (r RampIDDerived) Decode(ctx context.Context, userToken string) ([]byte, error) {
	return rampIDLookup(ctx, r.Client, userToken, "rampid_derived")
}

func rampIDLookup(ctx context.Context, client LiveRampClient, env, label string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("%s: LiveRamp client is not configured", label)
	}
	mapped, err := client.MappedID(ctx, env)
	if err != nil {
		if errors.Is(err, ErrLiveRampNoMapping) {
			return nil, ErrDropFromSeal
		}
		return nil, fmt.Errorf("%s: liveramp lookup: %w", label, err)
	}
	if mapped == "" {
		return nil, ErrDropFromSeal
	}
	return []byte(mapped), nil
}
