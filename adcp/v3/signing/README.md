# adcp/signing

RFC 9421 request-signing for the Ad Context Protocol.

Optional in AdCP 3.0 and capability-advertised via `request_signing` on `get_adcp_capabilities`. Required for spend-committing operations in AdCP 4.0.

Spec: [Signed Requests (Transport Layer)](https://adcontextprotocol.org/docs/building/implementation/security#signed-requests-transport-layer). Rolling out from unsigned to signed, or rotating keys: see [MIGRATION.md](./MIGRATION.md).

## Conformance

The package is validated against the spec's [conformance vectors](https://adcontextprotocol.org/compliance/latest/test-vectors/request-signing/):

- 13 / 13 positive vectors verify.
- 30 / 30 negative vectors reject with the exact `expected_outcome.error_code`.
- Ed25519 signatures produced by this signer match the committed positive-vector bytes byte-for-byte.
- `expected_signature_base` byte-diff passes on every positive vector (cross-implementation commitment check).

Vectors live under `testdata/request-signing/`; tests are in `conformance_test.go`.
The pinned suite includes the AdCP 3.1 base64url profile and the AdCP 3.2 RC
RFC 8941 profile. `ProfileRequestSigning` retains the 3.1-compatible default;
select `ProfileRequestSigningRC` only for a peer that explicitly negotiated
the 3.2 RC wire profile.

## Testing handlers that expect signed requests

The [`signingtest`](./signingtest) subpackage collapses the keypair + JWK +
`StaticJWKSResolver` + `NewMemoryReplayStore` boilerplate a handler test
otherwise has to hand-roll:

```go
signer, opts := signingtest.NewTestAgent(t)
opts.OperationResolver = signing.DefaultOperationResolver
opts.RequiredFor = []string{"create_media_buy"}
handler := signing.Middleware(opts)(yourHandler)

resp := signingtest.SignAndSend(t, signer, handler, req)
```

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

// Option C: bundled preset — Signer + RoundTripper + redirect-safe *http.Client in one call.
client, _ := signing.NewSignedHTTPClient(signing.SignedHTTPClientOptions{
    KeyID:              "my-agent-ed25519-2026",
    PrivateKey:         priv,
    CoverContentDigest: true,        // seller advertised covers_content_digest=required
    Timeout:            30 * time.Second,
})
```

Important: signed requests MUST NOT follow HTTP redirects — `@target-uri` coverage would bind the signature to the original URL, not the redirected one. `NewSignedHTTPClient` disables redirect-following for you; if you build the client by hand (Options A/B above), set `http.Client.CheckRedirect` to `func(...) error { return http.ErrUseLastResponse }` yourself.

The signer:

- canonicalizes `@target-uri` per the AdCP rules (strip default ports, `remove_dot_segments`, uppercase percent-hex, preserve query byte-for-byte),
- emits a ≥128-bit base64url-unpadded nonce,
- defaults to a 300-second validity window (profile max),
- attaches `Signature`, `Signature-Input`, and optionally `Content-Digest`,
- uses Ed25519 (deterministic) or ES256 with IEEE P1363 (`r||s`) encoding — **not** DER.

### Keeping the private key out of process memory (KMS / HSM / Vault)

`SignerOptions.PrivateKey` above is the default, in-memory path — fine for
tests and for operators who accept the key living in process RAM. The AdCP
spec recommends storing signing keys in an HSM or KMS instead. To do that,
implement `SigningProvider` and pass it as `SignerOptions.Provider` instead
of `KeyID`/`PrivateKey`:

```go
type SigningProvider interface {
    Sign(ctx context.Context, payload []byte) ([]byte, error)
    KeyID() string
    Algorithm() Algorithm
    PublicKey(ctx context.Context) (crypto.PublicKey, error)
}
```

`Sign` receives the RFC 9421 signature base bytes and `ctx` from the signed
`http.Request` (`SignRequest` passes `r.Context()`) — a KMS/HSM round trip
typically costs 10-50ms, and `ctx` is how a caller bounds that call's
deadline and cancellation. `InMemorySigningProvider` is what `NewSigner`
builds internally when `Provider` is nil; it remains the default.

```go
provider, _ := signing.NewInMemorySigningProvider("my-agent-ed25519-2026", priv) // or your own SigningProvider
signer, _ := signing.NewSigner(signing.SignerOptions{Provider: provider})
```

`PublicKey` is what makes two operational safeguards possible for *any*
`SigningProvider`, not just the in-memory one:

- **`NewPublicJWKFromProvider(ctx, provider, kid, adcpUse)`** builds the JWK
  to publish at `jwks_uri` directly from the provider, instead of
  hand-assembling `kty`/`crv`/`alg` (a common source of drift). `adcpUse` is
  required — pass `signing.ProfileRequestSigning.AdcpUse` or
  `signing.ProfileWebhookSigning.AdcpUse` — because the spec requires
  distinct key material per signing purpose
  ([adcontextprotocol/adcp#2423](https://github.com/adcontextprotocol/adcp/issues/2423)).
- **`AssertProviderPublicKeyMatchesSPKI(ctx, provider, expectedSPKI)`** is a
  startup tripwire: pin the expected SPKI bytes alongside deployed code and
  call this once after your listener binds. A managed key store can rotate
  the key backing a `kid` with zero signal to this SDK (an alias repointed,
  a Vault key rotated); left undetected, the Signer keeps producing
  signatures every verifier rejects. This fails loudly instead.

Errors an external provider's `Sign`/`PublicKey` return should be a
`*signing.SigningError` (`Code` is stable and safe to log; the raw backend
SDK error goes in `Wrapped`, reachable via `errors.Unwrap`/`errors.As` —
**never** in `Detail`, which must be a static, caller-controlled string).
KMS/HSM/Vault SDK errors routinely embed resource identifiers (key ARNs,
GCP resource paths, Vault mount paths) that must not flow into anything a
caller might echo into an HTTP response, per AGENTS.md's error-message rule.

A worked AWS KMS implementation ships as a separate module,
[`adcp/v3/signing/awskms`](./awskms) — separate specifically so that
importing `adcp/v3/signing` never pulls in `aws-sdk-go-v2` for callers who
don't use AWS KMS (see that module's `go.mod` and package doc for why, and
[AGENTS.md](../../../AGENTS.md)'s "Zero unnecessary dependencies" rule).

## Verifying (seller side)

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:            jwksResolver,
    // Replay defaults to a fresh NewMemoryReplayStore(0) when nil — fine
    // for single-instance verifiers. Wire an explicit shared store
    // (Redis, etc.) for multi-replica deployments.
    Revocation:          signing.NewStaticRevocationList(nil), // pass nil only in dev
    OperationResolver:   signing.DefaultOperationResolver,     // /adcp/<op>
    RequiredFor:         []string{"create_media_buy"},
    ContentDigestPolicy: signing.DigestRequired,
})
http.ListenAndServe(":8080", mw(yourHandler))
```

For MCP / JSON-RPC integrations that need a protocol-specific error envelope, supply an `OnReject` callback to translate the typed `*signing.Error` into your wire format instead of the default 401 text/plain response.

### Verifying from a handler

`VerifyRequest` is a tri-state wrapper around the lower-level `VerifyRequestSignature` and is the right call when your handler verifies directly (outside the middleware):

```go
switch res := signing.VerifyRequest(r, opts); res.Status {
case signing.StatusVerified:
    handleAuthenticated(res.Signer)
case signing.StatusUnsigned:
    handleBearerOnly(r)
case signing.StatusRejected:
    w.Header().Set("WWW-Authenticate", `Signature error="`+string(res.Error.Code)+`"`)
    http.Error(w, "unauthorized", http.StatusUnauthorized)
}
```

Use `VerifyRequestSignature` directly only where the `(signer, err)` shape is convenient. Its `(nil, nil)` return means "unsigned, not required" — `if _, err := VerifyRequestSignature(...); err == nil { trust(...) }` silently trusts unsigned requests. That misread is the bug `VerifyRequest` exists to make impossible.

When the middleware is permissive (no `RequiredFor` entry for an op) but a specific handler wants to gate its route on a signature — e.g., the requirement depends on a decoded body field — call `signing.RequireSigned(r.Context())` inside the handler:

```go
func handleCreate(w http.ResponseWriter, r *http.Request) {
    var body CreatePlanRequest
    _ = json.NewDecoder(r.Body).Decode(&body)
    if body.CommitsSpend {
        if err := signing.RequireSigned(r.Context()); err != nil {
            w.Header().Set("WWW-Authenticate", `Signature error="`+string(err.Code)+`"`)
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return
        }
    }
    // ...
}
```

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

`SigningProvider` (see above) keeps this true even for KMS/HSM-backed signing: it's an interface defined here with zero new imports, and any third-party SDK an implementation needs (`aws-sdk-go-v2`, for instance) lives in that implementation's own module — see [`adcp/v3/signing/awskms`](./awskms) — never in this package's dependency tree.
