# adcp/signing

RFC 9421 request-signing for the Ad Context Protocol.

Optional in AdCP 3.0 and capability-advertised via `request_signing` on `get_adcp_capabilities`. Required for spend-committing operations in AdCP 4.0.

Spec: [Signed Requests (Transport Layer)](https://adcontextprotocol.org/docs/building/implementation/security#signed-requests-transport-layer).

## Conformance

The package is validated against the spec's [conformance vectors](https://adcontextprotocol.org/compliance/latest/test-vectors/request-signing/):

- 8 / 8 positive vectors verify.
- 20 / 20 negative vectors reject with the exact `expected_outcome.error_code`.
- Ed25519 signatures produced by this signer match the committed positive-vector bytes byte-for-byte.
- `expected_signature_base` byte-diff passes on every positive vector (cross-implementation commitment check).

Vectors live under `testdata/request-signing/`; tests are in `conformance_test.go`.

## Signing (buyer side)

```go
pemBytes, _ := os.ReadFile("signing.pem")
priv, _, _ := signing.LoadPrivateKey(pemBytes)
signer, _ := signing.NewSigner(signing.SignerOptions{
    KeyID:      "my-agent-ed25519-2026",
    PrivateKey: priv,
})

// Option A: per-request.
req, _ := http.NewRequest("POST", "https://seller.example.com/adcp/create_media_buy", body)
req.Header.Set("Content-Type", "application/json")
_ = signer.SignRequest(req, signing.SignOptions{CoverContentDigest: true})

// Option B: signing round-tripper — every outbound request is signed automatically.
client := &http.Client{Transport: signer.RoundTripper(nil, true)}
```

Important: signed requests MUST NOT follow HTTP redirects — `@target-uri` coverage would bind the signature to the original URL, not the redirected one. Set `http.Client.CheckRedirect` to `func(...) error { return http.ErrUseLastResponse }` when using a signing client.

The signer:

- canonicalizes `@target-uri` per the AdCP rules (strip default ports, `remove_dot_segments`, uppercase percent-hex, preserve query byte-for-byte),
- emits a ≥128-bit base64url-unpadded nonce,
- defaults to a 300-second validity window (profile max),
- attaches `Signature`, `Signature-Input`, and optionally `Content-Digest`,
- uses Ed25519 (deterministic) or ES256 with IEEE P1363 (`r||s`) encoding — **not** DER.

## Verifying (seller side)

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:            jwksResolver,
    Replay:              signing.NewMemoryReplayStore(0),
    Revocation:          signing.NewStaticRevocationList(nil), // pass nil only in dev
    OperationResolver:   signing.DefaultOperationResolver,     // /adcp/<op>
    RequiredFor:         []string{"create_media_buy"},
    ContentDigestPolicy: signing.DigestRequired,
})
http.ListenAndServe(":8080", mw(yourHandler))
```

For MCP / JSON-RPC integrations that need a protocol-specific error envelope, supply an `OnReject` callback to translate the typed `*signing.Error` into your wire format instead of the default 401 text/plain response.

### Caveats

- **Body-modifying intermediaries break `content-digest` coverage.** CDNs, WAFs, and gateways that recompress or re-serialize request bodies will cause `request_signature_digest_mismatch`. Preserve body bytes end-to-end or use `DigestEither`.
- **Revocation polling is the caller's responsibility.** `StaticRevocationList` does not auto-refresh. Run a loop that calls `SetRevoked` on the cadence declared in the revocation list's `next_update` (floor 1 min, ceiling 15 min per spec).
- **Per-keyid replay cap defaults to 1,000,000.** Exceeding this rejects with `request_signature_rate_abuse` — a compromised-signer signal. Operators running sustained >3k QPS per signing key should deploy a distributed replay store.
- **Custom HTTPClient loses SSRF protection.** If you supply `HTTPJWKSResolver.HTTPClient`, start from `NewSafeHTTPClient()` to keep DNS rebinding / private-IP guards.
- **Signer buffers the body in memory** for digest computation, so large uploads are materialized in RAM. Chunked-encoding requests become Content-Length-terminated after signing.

On reject: `401 Unauthorized` with `WWW-Authenticate: Signature error="<code>"` (no realm). The `<code>` is one of the 17 stable codes in the [transport error taxonomy](https://adcontextprotocol.org/docs/building/implementation/security#transport-error-taxonomy).

On accept: `signing.VerifiedSignerFromContext(r.Context())` returns `*VerifiedSigner{KeyID, AgentURL, VerifiedAt, Algorithm}` so downstream handlers can authorize and audit by signed agent identity.

The verifier implements all 14 checks (13 numbered + 9a) of the [verifier checklist](https://adcontextprotocol.org/docs/building/implementation/security#verifier-checklist-requests):

1. parse `Signature-Input` / `Signature`
2. required sig-params present (`created`, `expires`, `nonce`, `keyid`, `alg`, `tag`)
3. `tag` == `adcp/request-signing/v1`
4. `alg` ∈ {`ed25519`, `ecdsa-p256-sha256`}
5. window (`expires > created`, ±60s skew, ≤ 300s max)
6. covered components (`@method`, `@target-uri`, `@authority`; `content-type` if body; `content-digest` per policy)
7. `keyid` resolves to a JWK via the configured `JWKSResolver`
8. JWK purpose (`use=sig`, `key_ops` contains `verify`, `adcp_use=request-signing`)
9. revocation (before crypto)
9a. per-keyid replay cache cap (before crypto)
10. cryptographic verify
11. `content-digest` recompute + compare (if covered)
12. replay dedup on `(keyid, nonce)`
13. insert into replay cache only after every prior step passed

## JWKS resolution

Two resolvers ship out of the box:

- `StaticJWKSResolver` — in-memory. Use for tests and deployments that manage key rotation outside the signing package.
- `HTTPJWKSResolver` — fetches JWKS over HTTPS with SSRF-safe dialing (blocks loopback / RFC 1918 / link-local / CGNAT / ULA) and a 30-second refetch cooldown between refetches on kid-miss. Pin your pre-validated agent URL → jwks_uri map at onboarding.

For custom deployments, implement the two-method `JWKSResolver` interface.

## Replay cache

`NewMemoryReplayStore(perKeyIDCap)` — LRU with TTL eviction and the profile's per-keyid entry cap (default 1,000,000). Implement the three-method `ReplayStore` interface to plug in Redis or another shared store for distributed deployments; the spec requires the step-13 insert to be atomic with a cap check in distributed setups to prevent cap drift.

## Key generation

```bash
go run github.com/adcontextprotocol/adcp-go/adcp/cmd/adcp-signing-keygen \
  -alg ed25519 \
  -kid my-agent-2026 \
  -out signing.pem
```

Writes a PKCS#8 PEM private key and prints a public JWK with `use=sig`, `key_ops=[verify]`, and `adcp_use=request-signing` ready for publication at `jwks_uri`.

## Dependencies

Zero third-party dependencies beyond the Go standard library. Canonicalization, JWK parsing for Ed25519 / ES256, and RFC 9421 / RFC 9530 encoding are implemented directly so the profile's exact byte semantics are under test, not delegated to a library whose defaults may drift.
