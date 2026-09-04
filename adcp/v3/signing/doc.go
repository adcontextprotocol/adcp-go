// Package signing implements the AdCP RFC 9421 request-signing profile:
// signs outgoing HTTP requests and verifies inbound ones for AdCP agent
// identity, with replay protection via per-(keyid, nonce) deduplication and
// tampering protection via optional RFC 9530 Content-Digest coverage.
//
// Covers: RFC 9421 signature base construction, RFC 9530 Content-Digest,
// Ed25519 and ECDSA P-256 (ES256) algorithms, nonce replay dedup, per-keyid
// revocation, SSRF-safe JWKS fetch, and advertisement via the
// `request_signing` capability block on `get_adcp_capabilities`.
//
// The profile is optional in AdCP 3.0 and required for spend-committing
// operations in AdCP 4.0.
//
// The wire tag `adcp/request-signing/v1` is part of the signed params. A
// future `v2` will reject v1 signatures and vice versa; upgrades require
// coordinated rollout.
//
// Reference: https://adcontextprotocol.org/docs/building/implementation/security#signed-requests-transport-layer
//
// # Signer
//
//	priv, _, _ := signing.LoadPrivateKey(pemBytes)
//	signer, _ := signing.NewSigner(signing.SignerOptions{
//	    KeyID:      "buyer-ed25519-2026",
//	    PrivateKey: priv,
//	})
//	if err := signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true}); err != nil { ... }
//
// # Verifier middleware
//
//	mw := signing.Middleware(signing.MiddlewareOptions{
//	    Resolver:            resolver,   // JWKSResolver
//	    Replay:              signing.NewMemoryReplayStore(0),
//	    Revocation:          revocation, // RevocationSource
//	    OperationResolver:   signing.DefaultOperationResolver, // /adcp/<op>
//	    ContentDigestPolicy: signing.DigestEither,
//	    RequiredFor:         []string{"create_media_buy"},
//	})
//	http.ListenAndServe(":8080", mw(handler))
//
// # Using the VerifiedSigner inside a handler
//
//	func handle(w http.ResponseWriter, r *http.Request) {
//	    v := signing.VerifiedSignerFromContext(r.Context())
//	    if v == nil {
//	        // unsigned request (operation not in RequiredFor) — proceed with bearer auth
//	    }
//	    // v.KeyID, v.AgentURL, v.VerifiedAt, v.Algorithm available for audit
//	}
//
// # Shadow-mode rollout
//
// MiddlewareOptions.ObserveOnly maps to the spec's warn_for rollout stop
// between supported_for and required_for: verification still runs, but a
// failing request passes to next.ServeHTTP anyway (no VerifiedSigner
// attached), and the failure is logged at INFO instead of rejected. See
// MIGRATION.md's "Step B — warn_for" for the full staged-enforcement recipe.
//
// # Testing handlers that expect signed requests
//
// See the signingtest subpackage for NewTestAgent and SignAndSend, which
// collapse the keypair + JWK + resolver + replay-store setup a handler test
// otherwise has to hand-roll.
package signing
