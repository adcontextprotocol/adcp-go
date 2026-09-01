# Migrating to signed AdCP requests

Rolling out RFC 9421 request signing against an existing AdCP integration is a two-track exercise: **bootstrap** once, then **enforce** in stages per operation. Key **rotation** follows the same pattern and is meant to be routine.

This guide covers the operator-facing mechanics. Spec reference: [Signed Requests (Transport Layer)](https://adcontextprotocol.org/docs/building/implementation/security#signed-requests-transport-layer).

## 1. Bootstrap

One-time work to make an agent able to sign (as a buyer) or verify (as a seller).

### Generate and publish a key

```bash
go run github.com/adcontextprotocol/adcp-go/adcp/cmd/adcp-signing-keygen \
  -alg ed25519 \
  -kid my-agent-2026-01 \
  -out signing.pem
```

Prefer Ed25519 over ES256 unless a regulatory constraint forces NIST curves. Ed25519 is deterministic by construction — no RNG participates at sign time, which simplifies replay analysis. ES256 via Go's `crypto/ecdsa` is also nonce-safe (the stdlib injects hedged deterministic nonces), but a non-Go counterparty without the equivalent guarantee carries the historical bad-RNG tail risk.

The command writes `signing.pem` (PKCS#8 private key) and prints a JWK with `use: "sig"`, `key_ops: ["verify"]`, and `adcp_use: "request-signing"`. Publish the JWK at your agent's `jwks_uri`:

```json
{ "keys": [ { "kid": "my-agent-2026-01", "kty": "OKP", ... } ] }
```

Hold the PEM in your secret store. If you need to serialize a JWK that carries a private half (for instance, when persisting a loaded key), call `JWK.Public()` before marshaling for publication — it zeros `d` / `_private_d_for_test_only`.

Production deployments that don't want the private key materialized in process memory at all can skip the PEM-on-disk step entirely: generate the key directly in a KMS/HSM (`aws kms create-key --key-usage SIGN_VERIFY --key-spec ECC_NIST_P256`, or equivalent), publish its public half as the JWK, and pass a `SigningProvider` — e.g. `adcp/v3/signing/awskms` — as `SignerOptions.Provider` instead of `KeyID`/`PrivateKey`. See the package [README](./README.md#keeping-the-private-key-out-of-process-memory-kms--hsm--vault).

### Advertise signing on `get_adcp_capabilities`

Set `request_signing` on your capabilities response with an empty `supported_for` / `warn_for` / `required_for` to start. Counterparties probing your capabilities can now see the block exists.

```json
"request_signing": {
  "supported_for": [],
  "warn_for":      [],
  "required_for":  []
}
```

## 2. Staged enforcement (per operation)

Never flip an operation straight from unsigned to required. Stage it through three stops:

### Step A — `supported_for`

Add the operation to `supported_for`. Counterparties MAY sign; your verifier MUST accept signed requests but does not yet reject unsigned ones.

Code — middleware stays permissive, but `Resolver` / `Replay` / `Revocation` are all wired:

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:          jwksResolver,
    Replay:            signing.NewMemoryReplayStore(0),
    Revocation:        signing.NewStaticRevocationList(nil),
    OperationResolver: signing.DefaultOperationResolver,
    RequiredFor:       nil, // nothing rejected yet
})
```

Success signal: signed requests arrive, `signing.VerifiedSignerFromContext(ctx)` returns non-nil on the verified path.

### Step B — `warn_for`

Move the operation to `warn_for`. Verification still runs and failures are logged; traffic is unaffected. Watch your failure rate and walk down the long tail of "some counterparty is misbehaving" before flipping to reject.

This is the spec's shadow-mode stop. The SDK exposes it via `MiddlewareOptions.ObserveOnly`: verification runs on every signed request, but a failure — including an unsigned request to an operation you've also (mistakenly, since `warn_for` and `required_for` are disjoint) left in `RequiredFor` — is logged at INFO instead of rejected, and the request reaches your handler with no `VerifiedSigner` in its context:

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:          jwksResolver,
    Replay:            signing.NewMemoryReplayStore(0),
    Revocation:        signing.NewStaticRevocationList(nil),
    OperationResolver: signing.DefaultOperationResolver,
    ObserveOnly:       true, // step-B: verify, log, never reject
    Logger:            logger,
})
```

One case still hard-rejects even under `ObserveOnly`: a partial or malformed `Signature`/`Signature-Input` header pair. Per spec, that pair "cannot be safely interpreted as either signed or unsigned traffic," so it 401s regardless of rollout stage — only a *well-formed* signature that fails verification (bad crypto, unknown key, expired window, replay, ...) is observed-and-passed-through.

Success signal: grep your logs for `"signature verification failed (ObserveOnly...)"` (field `code` = `request_signature_*`, `observe_only=true`) and watch the rate fall to zero — or to a known-and-tolerated set of counterparties — over a window long enough to cover your slowest integrator's deploy cadence.

### Step C — `required_for`

Move the operation to `required_for` and populate `MiddlewareOptions.RequiredFor`. Don't enable `DigestRequired` yet — body-modifying intermediaries are the most common surprise failure in production rollouts and they only break the digest path. Land `RequiredFor` first:

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver:          jwksResolver,
    Replay:            signing.NewMemoryReplayStore(0),
    Revocation:        signing.NewStaticRevocationList(nil),
    OperationResolver: signing.DefaultOperationResolver,
    RequiredFor:       []string{"create_media_buy"},
})
```

A rollout later, once you've confirmed no `request_signature_digest_mismatch` in step-B logs, tighten to `DigestRequired`:

```go
mw := signing.Middleware(signing.MiddlewareOptions{
    // ... same as above ...
    ContentDigestPolicy: signing.DigestRequired,
})
```

Unsigned or invalid requests are now rejected with `401 Unauthorized` + `WWW-Authenticate: Signature error="<code>"`.

### Rollback from step C

If production breaks after flipping `RequiredFor`:

1. Revert the middleware config — drop the operation from `MiddlewareOptions.RequiredFor` (and from `ContentDigestPolicy: DigestRequired` if set). Redeploy.
2. Do **not** touch `jwks_uri` or the revocation list. Counterparties that are already signing correctly will keep doing so, harmlessly.
3. Update `get_adcp_capabilities.request_signing.required_for` on the next deploy to match the rolled-back middleware — counterparties probing capabilities must not be told "required" while the verifier is back to permissive.

Returning to step B's shadow mode is the safe resting state while you diagnose.

## 3. Key rotation

Schedule rotation routinely — monthly to quarterly is the common range — so the path is exercised and a compromise-driven rotation isn't the first time you run it.

### Routine rotation

Two JWKS publishes plus a signer cutover:

1. **Publish new kid alongside old kid.** Update `jwks_uri` to list both keys. Wait for counterparties' JWKS caches to refresh — the SDK's `HTTPJWKSResolver` holds a 30-second refetch cooldown on kid-miss, so one minute is a safe floor.
2. **Cut over the signer.** Flip `SignerOptions.KeyID` / `PrivateKey` to the new kid on every instance.
3. **Grace period.** Hold both kids in the JWKS for at least **2× the max validity window** (10 minutes with the default 300-second window). Reasoning: one window for the last request you signed under the old kid to reach its `expires`, plus one window for its replay-cache entry to age out so you can't distinguish a real replay from a drained entry. Add operational headroom on top (1–2× the deploy cadence of your slowest counterparty) so in-flight retries under the old kid still verify.
4. **Remove the old kid.** Publish a JWKS with only the new kid.

### Compromise rotation

Ordering is different — the old kid must stop being trusted *before* anything else happens, because during routine step 1 ("publish both"), counterparties still accept signatures under the old kid for the full JWKS-propagation window:

1. **Revoke first.** Add the burned kid to your revocation list with an `effective_at` covering the compromise window. Counterparties polling the revocation list will reject anything signed by the burned kid, even if their JWKS cache hasn't refreshed yet.
2. **Publish the new kid.** Update `jwks_uri` to include the new kid. (The old kid can stay in the JWKS or be removed immediately — revocation beats presence.)
3. **Cut over the signer** to the new kid on every instance.
4. **Remove the old kid** from the JWKS once the compromise-window revocation entry is no longer needed. Keep the revocation entry active for at least the historical audit window (whatever your governance requires).

## 4. Common pitfalls

- **Body-modifying intermediaries break `content-digest` coverage.** CDNs, WAFs, and API gateways that recompress or re-serialize request bodies cause `request_signature_digest_mismatch`. Diagnose by comparing signer-side body bytes to verifier-side body bytes — they must be byte-identical. Either preserve bytes end-to-end or stay on `DigestEither` for the affected operation.
- **Forgetting `CheckRedirect` on signed clients.** `@target-uri` is part of the signature base. If the server 301s to a new URL, the signature still binds to the original URL. Set `http.Client.CheckRedirect` to `func(...) error { return http.ErrUseLastResponse }` on any client using the signing `RoundTripper`.
- **Clock skew > 60s.** Verifiers reject with `request_signature_window_invalid` when `created` is > 60s in the future or `expires` is > 60s in the past. NTP-sync both sides; investigate container hosts that drift after suspend/resume.
- **Custom `HTTPClient` losing SSRF protection.** If you supply `HTTPJWKSResolver.HTTPClient`, start from `signing.NewSafeHTTPClient()` — otherwise you lose the DNS-rebinding / private-IP / loopback guards, and an attacker who controls a `jwks_uri` can pivot against your internal network.
- **Per-keyid replay cap.** The default in-memory replay store caps at 1,000,000 entries per keyid. Sustained > 3k QPS per signing key will trip `request_signature_rate_abuse`. Deploy a distributed replay store (Redis or equivalent, tracked in [#54](https://github.com/adcontextprotocol/adcp-go/issues/54)) before you get there.
- **`ObserveOnly: true` that survives past step B.** It's a footgun if it survives into production unnoticed — it silently turns every required-signed op back into an unsigned op. Before flipping `RequiredFor` to enforce, grep your middleware wiring for `ObserveOnly` and confirm it's gone (or scoped to a different middleware instance covering only the operations still in `warn_for`). Months later, if you inherit the repo, re-grep.

## 5. Verification checklist before enforcing

- [ ] At least one signed request from a staging counterparty has been verified end-to-end — `signing.VerifiedSignerFromContext` returns non-nil in your handler — before flipping `required_for`.
- [ ] `jwks_uri` returns your current kid (and only your current kid, once rotation is complete).
- [ ] `get_adcp_capabilities.request_signing.required_for` matches `MiddlewareOptions.RequiredFor`.
- [ ] Revocation source is configured (not `nil` — the middleware logs a warning when `RequiredFor` is non-empty and `Revocation` is nil).
- [ ] Replay store is either in-memory (single instance) or a shared backing store (distributed).
- [ ] Logs from step B show zero unexpected failures over at least one full deploy cycle of your slowest counterparty.
- [ ] No `ObserveOnly: true` remains on the middleware instance you're about to enforce on (grep for `ObserveOnly`).
- [ ] `CheckRedirect` is set on every signing client.
- [ ] Clock sync monitoring is in place on signer and verifier hosts.
