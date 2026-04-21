package signing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadWebhookSpecKeys loads keys.json from the webhook-signing conformance
// suite. Separate from request-signing keys.json so the wrong-purpose and
// revoked kids line up with the webhook-specific negative vectors.
func loadWebhookSpecKeys(t *testing.T) *JWKS {
	t.Helper()
	data, err := os.ReadFile("testdata/webhook-signing/keys.json")
	require.NoError(t, err)
	jwks, err := ParseJWKS(data)
	require.NoError(t, err)
	return jwks
}

// buildWebhookResolver mirrors buildResolverForVector but sources keys from
// the webhook-signing key material.
func buildWebhookResolver(t *testing.T, v Vector) *StaticJWKSResolver {
	t.Helper()
	resolver := NewStaticJWKSResolver()
	var jwks *JWKS
	if v.JWKSOverride != nil {
		jwks = v.JWKSOverride
	} else {
		all := loadWebhookSpecKeys(t)
		jwks = &JWKS{}
		for _, kid := range v.JWKSRef {
			k := all.Find(kid)
			if k != nil {
				jwks.Keys = append(jwks.Keys, *k)
			}
		}
	}
	for i := range jwks.Keys {
		k := &jwks.Keys[i]
		resolver.Put(k.Kid, k, "https://signer.example.com/agent")
	}
	return resolver
}

// runWebhookVector runs a single webhook-signing vector through
// VerifyRequestSignature under ProfileWebhookSigning.
func runWebhookVector(t *testing.T, v Vector) (*VerifiedSigner, error) {
	t.Helper()
	req := buildRequestForVector(t, v)
	resolver := buildWebhookResolver(t, v)

	clock := func() time.Time { return time.Unix(v.ReferenceNow, 0).UTC() }
	replay := NewMemoryReplayStore(0)
	replay.withClock(clock)
	revocation := NewStaticRevocationList(nil)

	if v.HarnessState != nil {
		for _, e := range v.HarnessState.ReplayCacheEntries {
			nonceBytes, err := b64UrlDecode(e.Nonce)
			require.NoError(t, err)
			ttl := time.Duration(e.TTLSeconds) * time.Second
			if ttl <= 0 {
				// Webhook vectors omit ttl_seconds; the max signature validity
				// window is 300s per profile, so use that as the default.
				ttl = 300 * time.Second
			}
			replay.Preload(e.KeyID, string(nonceBytes), ttl)
		}
		// Webhook-shape state:
		if len(v.HarnessState.RevokedKids) > 0 {
			revocation.SetRevoked(v.HarnessState.RevokedKids)
		}
		if v.HarnessState.PerKeyIDCapFilledFor != "" {
			replay.MarkKeyIDAtCap(v.HarnessState.PerKeyIDCapFilledFor)
		}
		if v.HarnessState.RevocationListStaleSeconds > 0 {
			revocation.SetStale(true)
		}
		// Request-signing shape (for parity):
		if v.HarnessState.ReplayCacheCapHit != nil {
			replay.MarkKeyIDAtCap(v.HarnessState.ReplayCacheCapHit.KeyID)
		}
		if v.HarnessState.RevocationList != nil {
			revocation.SetRevoked(v.HarnessState.RevocationList.RevokedKids)
		}
	}

	return VerifyRequestSignature(req, VerifyOptions{
		Profile:             ProfileWebhookSigning,
		OperationName:       operationFromURL(v.Request.URL),
		RequiredFor:         v.Capability.RequiredFor,
		ContentDigestPolicy: DigestRequired, // webhook profile forbids opt-out
		Scheme:              "https",
		Resolver:            resolver,
		Revocation:          revocation,
		Replay:              replay,
		Clock:               clock,
	})
}

// TestWebhookPositiveVectors runs every positive webhook-signing vector
// through VerifyRequestSignature under ProfileWebhookSigning.
func TestWebhookPositiveVectors(t *testing.T) {
	dir := "testdata/webhook-signing/positive"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			v := loadVectorFile(t, filepath.Join(dir, name))
			signer, err := runWebhookVector(t, v)
			if !assert.NoError(t, err, "positive webhook vector %s must verify", name) {
				if e := AsError(err); e != nil {
					t.Logf("rejected with code=%s detail=%s", e.Code, e.Detail)
				}
				return
			}
			assert.NotNil(t, signer)
		})
	}
}

// TestWebhookNegativeVectors runs every negative webhook-signing vector and
// asserts byte-for-byte match on expected_outcome.error_code (which uses the
// webhook_signature_ prefix, not request_signature_).
func TestWebhookNegativeVectors(t *testing.T) {
	dir := "testdata/webhook-signing/negative"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			v := loadVectorFile(t, filepath.Join(dir, name))
			// Upstream authoring bug: vector 016's nonce "REPLAYED_________A"
			// decodes to ~13 bytes, below the 16-byte minimum both profiles
			// mandate (docs/building/implementation/security.mdx — "Verifiers
			// MUST reject if the decoded byte length is less than 16 bytes").
			// Our spec-compliant rejection lands on params_incomplete before
			// the replay check. The request-signing 016 vector uses a proper
			// 16-byte nonce and passes; the webhook suite hand-authored a
			// human-readable stand-in that falls short. Filed upstream.
			if name == "016-replayed-nonce.json" {
				t.Skip("upstream vector 016 has a sub-16-byte test nonce; spec requires 16+")
			}
			_, err := runWebhookVector(t, v)
			require.Error(t, err, "negative webhook vector %s must reject", name)
			e := AsError(err)
			require.NotNil(t, e, "vector %s: want *Error, got %v", name, err)
			assert.Equal(t, v.Expected.ErrorCode, e.WireCode(ProfileWebhookSigning), "vector %s wrong error code", name)
		})
	}
}
