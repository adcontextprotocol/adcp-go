package main

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"

	"github.com/adcontextprotocol/adcp-go/targeting/identityagent"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/adcontextprotocol/adcp-go/verify/worldid"
)

// verifiedIdentityOptions builds the RunOptions that turn on the TMP
// verified-identity receiver path. It is gated on TMP_VERIFIED_IDENTITY_ENABLED
// and returns no options when unset, so the default deployment is fail-closed:
// no verifier is wired, every attestation is treated as absent, and eligibility
// is unchanged.
//
// Enabling a verifier is a coordinated rollout — the impression writer must emit
// RP-namespaced `vid:<rp>:<nullifier>` caps before this is switched on, or
// frequency caps read empty (fail open) for the verified path.
//
// Required when enabled:
//
//	TMP_RELYING_PARTY_ID  the relying party this deployment acts as; must match
//	                      the rp_id the sealed credentials were sealed for.
//	TMP_RECIPIENT_KID     the audience_kid identifying our HPKE recipient key.
//	TMP_RECIPIENT_KEY     hex-encoded 32-byte X25519 private key (secret —
//	                      injected from secret storage, never logged).
//
// Optional:
//
//	WORLD_VERIFY_BASE_URL World's verifier backend (defaults to production).
//	WORLD_API_KEY         API key for World's verifier backend, if required.
//
// Age gating (AgeResolver) is intentionally left unset: production resolves the
// required-age policy via the AdCP Policy Registry, which is not yet wired. With
// no resolver, no age gating is applied — age-restricted packages are simply not
// served through this path rather than being gated on an unverified claim.
func verifiedIdentityOptions(logger *slog.Logger) ([]identityagent.RunOption, error) {
	enabled, err := lookupBool("TMP_VERIFIED_IDENTITY_ENABLED", false)
	if err != nil {
		return nil, err
	}
	if !enabled {
		logger.Info("verified-identity stage disabled (set TMP_VERIFIED_IDENTITY_ENABLED=true to enable)")
		return nil, nil
	}

	rpID := os.Getenv("TMP_RELYING_PARTY_ID")
	kid := os.Getenv("TMP_RECIPIENT_KID")
	keyHex := os.Getenv("TMP_RECIPIENT_KEY")
	switch {
	case rpID == "":
		return nil, fmt.Errorf("TMP_RELYING_PARTY_ID is required when TMP_VERIFIED_IDENTITY_ENABLED=true")
	case kid == "":
		return nil, fmt.Errorf("TMP_RECIPIENT_KID is required when TMP_VERIFIED_IDENTITY_ENABLED=true")
	case keyHex == "":
		return nil, fmt.Errorf("TMP_RECIPIENT_KEY is required when TMP_VERIFIED_IDENTITY_ENABLED=true")
	}

	raw, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("TMP_RECIPIENT_KEY must be hex-encoded: %w", err)
	}
	key, err := tmproto.LoadX25519PrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("TMP_RECIPIENT_KEY: %w", err)
	}

	baseURL := worldid.DefaultBaseURL
	if v := os.Getenv("WORLD_VERIFY_BASE_URL"); v != "" {
		baseURL = v
	}
	verifier := worldid.New(
		worldid.WithBaseURL(baseURL),
		worldid.WithAPIKey(os.Getenv("WORLD_API_KEY")),
	)

	logger.Info("verified-identity stage enabled",
		"relying_party_id", rpID, "recipient_kid", kid, "world_verify_base_url", baseURL)

	return []identityagent.RunOption{
		identityagent.WithAttestationVerifier(verifier),
		identityagent.WithRecipientKeys(map[string]identityagent.RecipientKey{
			kid: {PrivateKey: key, RelyingPartyID: rpID},
		}),
	}, nil
}

// lookupBool parses a boolean environment variable, returning def when unset.
func lookupBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	switch v {
	case "1", "true", "TRUE", "True":
		return true, nil
	case "0", "false", "FALSE", "False":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean (got %q)", key, v)
	}
}
