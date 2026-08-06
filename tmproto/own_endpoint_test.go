package tmproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOwnEndpointURLConventionMatchesRouterSigning is the regression test for
// the convention mismatch: a router signs provider_endpoint_url from the
// registered BASE endpoint and appends the operation path only when dispatching,
// so a provider that verifies against the path-inclusive URL rejects every
// request. This pins both halves against each other in one place.
func TestOwnEndpointURLConventionMatchesRouterSigning(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := NewSigner("kid-convention", priv)
	require.NoError(t, err)
	ks := NewStaticKeyStore([]SigningKey{signer.PublicJWK()})

	// What a router has in its provider registration.
	const registeredEndpoint = "https://identity-agent.example.com"
	// What the router actually POSTs to.
	const dispatchURL = registeredEndpoint + "/identity"

	req := &IdentityMatchRequest{
		Type:           TypeIdentityMatchRequest,
		RequestID:      "id-convention",
		SellerAgentURL: "https://seller.example.com/agent",
		Identities:     []IdentityToken{{UserToken: "tok_abc", UIDType: "uid2"}},
	}
	sig, err := signer.SignIdentityMatch(req, registeredEndpoint, CurrentEpoch())
	require.NoError(t, err)

	// The base URL verifies — this is the convention agents must configure.
	require.NoError(t, VerifyIdentityMatch(req, registeredEndpoint, sig, signer.KeyID, ks, time.Now()),
		"the registered base URL is what the router binds the signature to")

	// The dispatch URL does not. This is the failure an agent gets when it is
	// configured with the URL the router POSTs to instead of the base URL the
	// router signs over — silent at startup, total at runtime. The shipped agent
	// examples documented the wrong one; see cmd/*-agent/example.*.env.
	assert.ErrorIs(t, VerifyIdentityMatch(req, dispatchURL, sig, signer.KeyID, ks, time.Now()), ErrSignatureInvalid,
		"configuring the path-inclusive URL must not verify")
	// targeting/internal/tmpendpoint rejects this at agent startup so the
	// mismatch surfaces there instead of as a total runtime failure.
}
