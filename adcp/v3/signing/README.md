# adcp/signing

RFC 9421 request-signing for the Ad Context Protocol.

Optional in AdCP 3.0 and capability-advertised via `request_signing` on `get_adcp_capabilities`. Required for spend-committing operations in AdCP 4.0.

Spec: [Signed Requests (Transport Layer)](https://adcontextprotocol.org/docs/building/implementation/security#signed-requests-transport-layer). Rolling out from unsigned to signed, or rotating keys: see [MIGRATION.md](./MIGRATION.md).

> **AdCP 3.3: two signing profiles.** The AdCP RFC 9421 profile (`adcp/request-signing/v1`, keys through brand.json) is **deprecated in AdCP 3.3**. It keeps working through 3.x and is removed in 4.0. The **Web Bot Auth profile** (`web-bot-auth`) is experimental in 3.3 and opt-in here: see [Web Bot Auth](#web-bot-auth-experimental-adcp-33) below and the [agent identity migration guide](../../../docs/agent-identity-3-3-migration.md). `Middleware` logs the deprecation once per process unless `WebBotAuth` is configured.

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

## Response size limits

Agent responses are untrusted input. `NewSignedHTTPClient` caps every response
at **4 MiB** (`signing.DefaultMaxResponseBytes`) by default — no opt-in
required. A seller that answers `get_products` with a multi-megabyte body
fails the call instead of driving the caller out of memory.

```go
client, _ := signing.NewSignedHTTPClient(signing.SignedHTTPClientOptions{
    KeyID:            "my-agent-ed25519-2026",
    PrivateKey:       priv,
    MaxResponseBytes: 32 << 20, // this seller publishes a large catalog
    MaxSSEEventBytes: 1 << 20,  // but no single SSE event should be that big
})
```

Per call, for the one operation that needs the headroom:

```go
ctx := signing.WithResponseLimits(ctx, signing.ResponseLimits{MaxResponseBytes: 32 << 20})
req, _ := http.NewRequestWithContext(ctx, "POST", agentURL, body)
resp, err := client.Do(req)
```

What the cap guarantees:

- **Enforced while reading, never truncating.** An oversized `Content-Length`
  is rejected before the body is read at all; otherwise the read that crosses
  the limit fails. At most `limit + 1` bytes are ever pulled from the peer,
  and the caller always gets an error — a short body never looks like a
  complete one.
- **Typed error.** Every rejection is a `*signing.ResponseTooLargeError` and
  matches `errors.Is(err, signing.ErrResponseTooLarge)`. `Scope` says whether
  the whole body or a single SSE event tripped it; `Limit`, `Read`, and
  `ContentLength` carry the numbers. A pre-reject surfaces from `client.Do`
  (wrapped in `*url.Error`); a mid-stream reject surfaces from the read.
- **SSE is covered.** A `text/event-stream` response is capped per event
  (`MaxSSEEventBytes`, which inherits `MaxResponseBytes`) *and* cumulatively
  per call (`MaxResponseBytes`), so a peer cannot escape the limit by
  answering with a stream. Whatever decodes the body — `bufio.Scanner`, an SSE
  library, an MCP streamable-HTTP client — gets both checks from the wrapper
  with no SSE-specific wiring, because the counting happens under
  `resp.Body.Read`. A long-lived stream that legitimately runs past
  `MaxResponseBytes` over its lifetime needs an explicit higher cumulative cap
  (or `signing.UnlimitedResponseBytes`) with `MaxSSEEventBytes` kept tight.
- **Decompressed bytes count.** The cap sits outside `net/http`'s transparent
  gzip, so a body that is 100 KiB on the wire and 8 MiB inflated is rejected
  at 8 MiB. Because net/http only decompresses the gzip it negotiated itself,
  a capped request that carries its own `Accept-Encoding` naming a
  compressing coding has that header dropped so negotiation — and the
  inflation — happens under the counter. `Accept-Encoding: identity` is left
  alone, as is any request whose cap is disabled.
- **Opt-out is explicit.** `MaxResponseBytes: signing.UnlimitedResponseBytes`
  (any negative value) turns the cap off. Zero means "use the default", so
  leaving the field unset keeps you protected. Since `MaxSSEEventBytes`
  inherits `MaxResponseBytes`, opting out of the body cap opts out of the
  per-event cap as well — set `MaxSSEEventBytes` explicitly to keep events
  bounded while the call runs unbounded.

Two more responses the cap rejects outright, because their size cannot be
accounted for: a `Content-Encoding` the peer was never offered (anything but
`identity` that net/http did not inflate itself) fails with
`*signing.UnsupportedContentEncodingError`, matching
`errors.Is(err, signing.ErrUnsupportedContentEncoding)`.

Response *headers* are bounded too, but by the transport rather than this
wrapper: clients from `NewSignedHTTPClient` use a clone of
`http.DefaultTransport` with `MaxResponseHeaderBytes` lowered from net/http's
10 MiB default to `signing.DefaultMaxResponseHeaderBytes` (64 KiB), so a peer
cannot spend megabytes on headers the body cap never sees. The clone carries
its own connection pool, so reuse one client per peer instead of building one
per call. A transport you pass as `Inner` is used exactly as given — set
`MaxResponseHeaderBytes` on it yourself.

Building the client yourself?
`signing.NewResponseLimitTransport(inner, signing.ResponseLimits{})` applies
the same cap to any `http.RoundTripper`, including the lower-level
`(*Signer).RoundTripper`, which does not cap on its own.

### Handling the rejection

- **The peer picks which surface fails.** Declaring an oversized
  `Content-Length` makes `client.Do` return the error with a nil response;
  omitting it makes the read fail instead. Handle both — a caller that only
  checks one can be steered into the other.
- **Log the error, don't echo it.** `client.Do` wraps the rejection in
  `*url.Error`, whose message includes the full request URL and query. Follow
  the usual rule for a service boundary: `slog.Error(...)` with the error,
  `"internal error"` on the wire.
- **The cap does not make padding visible.** `json.NewDecoder(resp.Body).Decode(&v)`
  stops at the first complete JSON value, so megabytes of trailing junk are
  never read and never counted. Call `dec.More()` afterwards if you want to
  reject a padded response rather than ignore its tail.
- **HTTP/2 buffers below the cap.** Under h2, net/http's per-stream flow
  control window (4 MiB) is in flight regardless of this limiter, and the
  connection window is far larger. At crawler fan-out, tune the transport's
  HTTP/2 settings as well.

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
6. covered components (`@method`, `@target-uri`, `@authority`; `content-type` if body; `content-digest` per policy). These are a minimum: a signer may also cover other HTTP fields. Each extra field must be present on the request; its value (every field line, trimmed and joined with `", "` per RFC 9421 §2.1) is bound into the signature like any other component. A header is authenticated only if it appears in `VerifiedSigner.CoveredComponents`. Duplicate components, derived components outside the profile (`@path`, `@query`, ...), and component parameters (including the Web Bot Auth `"signature-agent";key=...` member form, which this RFC 9421 verifier does not handle) are rejected. The `Signer` never covers extra fields.
7. `keyid` resolves to a JWK via the configured `JWKSResolver`
8. JWK purpose (`use=sig`, `key_ops` contains `verify`, `adcp_use=request-signing`)
9. revocation (before crypto)
9a. per-keyid replay cache cap (before crypto)
10. cryptographic verify
11. `content-digest` recompute + compare (if covered)
12. replay dedup on `(keyid, nonce)`
13. insert into replay cache only after every prior step passed

## Web Bot Auth (experimental, AdCP 3.3)

The [Web Bot Auth profile](https://adcontextprotocol.org/docs/building/by-layer/L1/wba-profile) identifies a signer by its origin. The signer names the origin in a covered `Signature-Agent` member, whose value must be exactly the canonical `https` origin: a lowercase host, no default port, and no trailing slash, path, query, fragment, or userinfo (WBA §5.2.1 and §5.5). The verifier refuses any other value with `403` before it fetches a directory. The verifier fetches the origin's key directory at `/.well-known/http-message-signatures-directory` and looks keys up by `(origin, RFC 7638 thumbprint)`. There is no capabilities call and no keyid-to-agent map. Everything here is opt-in, and the default signer never emits or covers `Signature-Agent`.

Buyer side:

```go
signer, _ := signing.NewSigner(signing.SignerOptions{
    PrivateKey:  priv,                         // KeyID is derived: the key's thumbprint
    AgentOrigin: "https://buyer.example.com", // selects Web Bot Auth
})
// Publish the key directory on that origin, re-signed before it expires:
pub, _ := signing.NewDirectoryPublisher(signing.DirectoryPublisherOptions{
    AgentOrigin: "https://buyer.example.com", PrivateKeys: []any{priv},
})
mux.Handle(signing.DirectoryPath, pub)
```

`NewSignedHTTPClient` takes `AgentOrigin` too. With a `CapabilityProvider` it also needs a `ProfilesProvider`, and it refuses to send (`ErrWebBotAuthNotAdvertised`) unless the seller lists `web-bot-auth` in `request_signing.profiles`. It never falls back to RFC 9421 on its own. `go run github.com/adcontextprotocol/adcp-go/adcp/v3/cmd/adcp-wba-directory` writes a signed directory for static hosting.

Seller side:

```go
keys := signing.NewDirectoryResolver(signing.DirectoryResolverOptions{})
revocations, _ := signing.NewOriginRevocationChecker(signing.OriginRevocationCheckerOptions{Keys: keys})
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver: rfc9421Resolver, // the RFC 9421 profile keeps working
    WebBotAuth: &signing.WebBotAuthOptions{
        Keys:       keys,
        Revocation: revocations,
        Authorizer: &identity.BindingGate{Bindings: bindings, Records: trustRecords},
        Authorities: []string{"seller.example.com"}, // refuse cross-seller replays
    },
    BoundOrigins: bindings, // no downgrade to RFC 9421 for a bound origin
})
```

A seller that advertises only `web-bot-auth` in `request_signing.profiles` sets `WebBotAuthOptions.Exclusive`: every signed request is verified under Web Bot Auth, so an RFC 9421 signature is refused with `403` and `Accept-Signature`, and `Resolver` and `BoundOrigins` are unused. `ObserveOnly` then lets through only unsigned requests to `RequiredFor` operations; a failed signature is always rejected.

- **Profile selection.** A request carrying `Signature-Agent` or a `web-bot-auth` tag is verified under Web Bot Auth only, even under `ObserveOnly`. An uncovered `Signature-Agent` member is never attributed.
- **One signature.** A request carries exactly one `web-bot-auth` signature and exactly one `Signature-Agent` member. More than that is refused with `403` and `Accept-Signature`, even when every signature verifies on its own: an intermediary that signs a request is its signer, and whether it may act for someone else is an authorization question. Signatures under other tags are not read.
- **Verification.** It needs only the key directory and replay state, so `VerifyWebBotAuthRequest` can run in front of the server. `created`, `expires` (300 s, 60 s skew), `keyid`, `alg`, `nonce`, and `tag` are required, and digest coverage on a body is enforced. Only the `web-bot-auth` signature is read, but a `Signature` member that is not an unparameterized byte sequence, or a `Signature-Input` member that is not an inner list, is `400` under any label (RFC 9421 §4.1 and §4.2), as Cloudflare's `http-message-sig` answers. `alg` is `ed25519` and must match the key: the profile signs and verifies with Ed25519 only. RSA keys are not Web Bot Auth keys, and P-256 keys are not either (ES256 stays on the RFC 9421 profile); a directory that lists one skips it. The nonce is 64 bytes and is scoped to the identity. The profile states no preference for signers, and this signer emits 64 random bytes as unpadded base64url. The verifier also accepts the padded standard base64 that Cloudflare's library and the WBA architecture draft's own vectors use. These are the two forms Cloudflare's `validateNonce` accepts; padded base64url and any other length are refused. `@target-uri` is the target URI as received, percent-encoding included (RFC 9421 §2.2.2), and `@authority` is normalized (§2.2.3).
- **Unsigned requests.** An unsigned request needs a signature when it names an operation in `RequiredFor` or a method in `ProtocolMethodsRequiredFor` (read by exact member name, as the MCP SDK dispatches), carries `push_notification_config.authentication` under any letter case, or has a body that is not strict JSON (duplicate or case-variant member names), which no check can read the way the handler will. It gets the answer for what the verifier advertises. With both profiles, it is the RFC 9421 answer (`401`, `request_signature_required`) plus `Accept-Signature` for Web Bot Auth. With `Exclusive`, it is `403` with `Accept-Signature`. `AcceptSignature(body)` returns the value.
- **Key directories.** A directory is served as `application/http-message-signatures-directory+json` (WBA §5.5.1); any other media type is refused. The profile requires signed directories (WBA Appendix B.1): one signature per usable key, covering `"@authority";req` and `content-digest`, tagged `http-message-signatures-directory`, inside its window with no clock skew. An unsigned directory, or one with a key that did not sign it, is refused. Keys the profile cannot use are skipped and need no signature. A directory that fails to resolve (network or TLS error, timeout, any status other than 200, an invalid directory, or a fetch the resolver's own limits refuse) is a **discovery failure** (WBA §6.10). It never evicts the cached directory, whose keys keep verifying for up to `MaxStale` (24 h by default, never past the directory signatures' expiry; one more 5-minute cache period when the refusal is the resolver's own). With no usable cached copy, the request is unverified: `IsDiscoveryFailure(err)`. `WebBotAuthOptions.OnDiscoveryFailure` is the local policy. The default answers 403 with `Accept-Signature`; `DiscoveryFailureAnonymous` serves the request as anonymous, except for operations in `RequiredFor`. A directory that resolves without the key is evidence that the key was removed.
- **Errors, split by layer.** The signature path answers only with the profile's statuses and headers, never `request_signature_*` codes and never 503: `400` when a field doesn't parse; `429` with `Accept-Signature` carrying a fresh nonce for a reused signature or nonce; `403` with `Accept-Signature` for every other signature failure; and `403` without it for a revoked key. A replay store at its per-identity capacity answers `429` with `Retry-After` and no `Accept-Signature`. Any body is the one generic authentication error (`AUTH_INVALID`). Authorization outcomes after verification use the AdCP error envelope with no `Accept-Signature`:
  - `PERMISSION_DENIED` (403, no details) for a failed binding or an unbound origin calling a signature-required operation;
  - `SERVICE_UNAVAILABLE` (503, `Retry-After`) for a spend-committing call while the trust record or the origin's revocation list can't be read, per the outage rules in the `identity` package.
- **Revocation is authorization.** `WebBotAuthOptions.Revocation` runs `CheckWBARevocation` after verification and records each signer's state in `VerifiedSigner.Revocation`. A revoked key is refused (403, no `Accept-Signature`). An origin with no list (`RevocationNoList`) is identity-established and can't be bound. A list that can't be read or is past grace (`RevocationStale`) fails spend-committing calls closed in `identity.BindingGate` and `identity.GrantGate`; other calls proceed, and the last verified list keeps revoking. A revocation store that can't be read (`RevocationUnavailable`) fails every bound call with `SERVICE_UNAVAILABLE`.
- **Authority.** A verified origin with no binding is *identity-established* and gets what an anonymous caller gets. Operations in `WebBotAuthOptions.UnboundFor` (default `get_adcp_capabilities` and `get_principal`), the MCP session methods, and bodyless streamable-HTTP `GET`/`DELETE` run with the identity attached. Other operations are served as anonymous requests. Bindings and trust-record evaluation live in the [`identity`](../identity) package, which this package never imports, so the signature path cannot read a trust record.
- **MCP handlers.** Read the signer with `VerifiedSignerFromToolRequest(req)`. A stateful MCP session binds handlers to its `initialize` request's context, so the middleware also carries every verified signer, under either profile, in the per-request `auth.TokenInfo` (only `Extra` is set, never `UserID`). Install the middleware inside any bearer-token middleware.
- **Bodies.** A non-empty signed body must be strict JSON. Keys that differ only in case count as duplicates. Anything else gets `400`.

`VerifyWebBotAuthRequest` is the standalone verifier; it returns the one verified signer. `VerifiedSigner.Profile`, `PrincipalID`, `Revocation`, and `CoveredComponents` report what verified. The profile's vectors under `testdata/wba-profile/` (adcontextprotocol/adcp#7894 at `e5929470`, pinned by checksum in `UPSTREAM.json`) all pass: both positive vectors verify, all six negative vectors get their expected status (`400`, `403`, or `429`) and the `Accept-Signature` the page shows, and the signer reproduces the positive signature byte for byte. The interop fixtures under `testdata/wba-interop/` (from adcontextprotocol/adcp-client, signed by Cloudflare's `web-bot-auth@0.2.0` and by the TypeScript SDK; provenance in `UPSTREAM.json`) verify too, percent-encoded path included, and the Go signer reproduces the TypeScript-signed requests byte for byte.

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
