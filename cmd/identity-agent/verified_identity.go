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
//	TMP_RELYING_PARTY_ID  the relying party this deployment acts as. In-band
//	                      attestations on req.Identities must carry this rp_id,
//	                      and sealed credentials must be sealed for it.
//
// Optional:
//
//	TMP_RECIPIENT_KID     the audience_kid identifying our HPKE recipient key.
//	TMP_RECIPIENT_KEY     hex-encoded 32-byte X25519 private key (secret —
//	                      injected from secret storage, never logged). KID and
//	                      KEY are set together to enable the sealed_credentials
//	                      carrier; without them only in-band attestations are
//	                      verified.
//	WORLD_VERIFY_BASE_URL World's verifier backend (defaults to production).
//	WORLD_API_KEY         API key for World's verifier backend, if required.
//	TMP_WORLD_ID_TRUST_UNVERIFIED
//	                      Demo only. When true, attestations are trusted from
//	                      their own self-reported nullifier and claims without
//	                      validating the World ID proof or calling World — no
//	                      Sybil resistance or proof-of-personhood. Default false.
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
	if rpID == "" {
		return nil, fmt.Errorf("TMP_RELYING_PARTY_ID is required when TMP_VERIFIED_IDENTITY_ENABLED=true")
	}
	kid := os.Getenv("TMP_RECIPIENT_KID")
	keyHex := os.Getenv("TMP_RECIPIENT_KEY")
	if (kid == "") != (keyHex == "") {
		return nil, fmt.Errorf("TMP_RECIPIENT_KID and TMP_RECIPIENT_KEY must be set together (the sealed_credentials carrier needs both)")
	}

	trustUnverified, err := lookupBool("TMP_WORLD_ID_TRUST_UNVERIFIED", false)
	if err != nil {
		return nil, err
	}

	opts := []identityagent.RunOption{
		identityagent.WithRelyingPartyID(rpID),
	}
	if trustUnverified {
		logger.Warn("WORLD ID ATTESTATIONS TRUSTED WITHOUT VERIFICATION — TMP_WORLD_ID_TRUST_UNVERIFIED=true; demo only, no Sybil resistance or proof-of-personhood, never enable on production traffic")
		opts = append(opts, identityagent.WithAttestationVerifier(worldid.NewTrustingVerifier()))
	} else {
		baseURL := worldid.DefaultBaseURL
		if v := os.Getenv("WORLD_VERIFY_BASE_URL"); v != "" {
			baseURL = v
		}
		opts = append(opts, identityagent.WithAttestationVerifier(worldid.New(
			worldid.WithBaseURL(baseURL),
			worldid.WithAPIKey(os.Getenv("WORLD_API_KEY")),
		)))
	}

	// Recipient keys enable the sealed_credentials carrier; without them only
	// the in-band attestation carrier (req.Identities[].Attestation) is active.
	if kid != "" {
		raw, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("TMP_RECIPIENT_KEY must be hex-encoded: %w", err)
		}
		key, err := tmproto.LoadX25519PrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("TMP_RECIPIENT_KEY: %w", err)
		}
		opts = append(opts, identityagent.WithRecipientKeys(map[string]identityagent.RecipientKey{
			kid: {PrivateKey: key, RelyingPartyID: rpID},
		}))
	}

	logger.Info("verified-identity stage enabled",
		"relying_party_id", rpID, "sealed_credentials_enabled", kid != "", "trust_unverified", trustUnverified)

	return opts, nil
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
