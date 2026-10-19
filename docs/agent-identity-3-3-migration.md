# Agent identity in AdCP 3.3: adcp-go migration note

This note covers the experimental AdCP 3.3 agent-identity support in `github.com/adcontextprotocol/adcp-go/adcp/v3` (issue [#556](https://github.com/adcontextprotocol/adcp-go/issues/556)). It is the adcp-go entry for the SDK table in the spec's migration guide, [Migrating agent identity in AdCP 3.3](https://adcontextprotocol.org/docs/reference/migration/agent-identity-3-3).

Everything here is **opt-in and experimental**.
- **Wire pin.** It is unchanged: `adcp/v3/schemas/VERSION` stays at 3.2.x, and nothing advertises 3.3.
- **Pinned drafts.** The Web Bot Auth wire rules and vectors are pinned to adcontextprotocol/adcp#7894 at `e5929470` (provenance and checksums in [`adcp/v3/signing/testdata/wba-profile/UPSTREAM.json`](../adcp/v3/signing/testdata/wba-profile/UPSTREAM.json)). The identity schemas are pinned to the draft commits in [`adcp/v3/identity/testdata/schemas/UPSTREAM.json`](../adcp/v3/identity/testdata/schemas/UPSTREAM.json).
- **Change.** Both may still change before 3.3 ships.
- **Default behavior.** Existing APIs behave exactly as before unless you set the new options.

## What is where

| Need | API |
|---|---|
| Sign as a Web Bot Auth agent | `signing.SignerOptions.AgentOrigin` (or `SignWebBotAuthRequest`), `SignedHTTPClientOptions.AgentOrigin` |
| Publish the key directory | `signing.NewDirectoryPublisher` (an `http.Handler`), `signing.SignKeyDirectory`, `cmd/adcp-wba-directory` |
| Verify Web Bot Auth | `signing.MiddlewareOptions.WebBotAuth`, or `signing.VerifyWebBotAuthRequest` |
| Resolve many signers by `Signature-Agent` | `signing.NewDirectoryResolver` (keyed by origin and RFC 7638 thumbprint) |
| Origin revocation lists | `signing.NewOriginRevocationChecker`, `signing.RevocationDetails` |
| Canonical origin | `signing.CanonicalOrigin` |
| trust.json authoring and validation | `identity.ValidateTrustRecord`, `identity.MarshalTrustRecord` |
| Trust-record evaluation | `identity.NewTrustRecordEvaluator`, `identity.TrustRecordURL`, `identity.AuthorityHistory` |
| Bind an origin to a principal | `identity.TrustRecordEvaluator.Bind`, `identity.OriginBindings` (in memory), `identity.BindingGate` |
| Grants at `sync_accounts` (DR-0025) | `identity.GrantEvaluator`, `identity.GrantGate`, `adcp.Config.SyncAccountsAuthorizer` |
| New schema types | `identity.AgentOnboarding` and its tiers, requirements, and enums; `identity.BuyerOnboardingPosture`; `identity.PrincipalRelationship`; `identity.PrincipalChanged*` reasons; `identity.GrantAbsentDetails`; `identity.TrustRecord`; `identity.RequestSigningCapabilities` (`profiles`) |

## Buyer signer

1. Give the agent its own HTTPS origin, one agent per origin.
2. Sign with the Web Bot Auth profile:

   ```go
   priv, _, _ := signing.LoadPrivateKey(pemBytes) // Ed25519 only: RSA and P-256 keys are refused
   client, _ := signing.NewSignedHTTPClient(signing.SignedHTTPClientOptions{
       PrivateKey:         priv,
       AgentOrigin:        "https://buyer.example.com",
       CapabilityProvider: capsFor,     // the seller's request_signing block
       ProfilesProvider:   profilesFor, // the seller's request_signing.profiles
   })
   ```

   `AgentOrigin` sets and covers `Signature-Agent`. The signature carries `tag="web-bot-auth"`, a 64-byte random nonce (unpadded base64url), the key's thumbprint as `keyid`, and `alg="ed25519"`. Ed25519 is the profile's only algorithm, so an RSA or P-256 key cannot sign under Web Bot Auth; keep a P-256 key for RFC 9421. A request carries exactly one Web Bot Auth signature, so an intermediary that forwards a request for someone else signs it as itself, and the brand's `brand.json` or `trust.json` says whether it may. `@target-uri` is the URI the request is sent to, unchanged. The digest is always covered on a body. With a `CapabilityProvider`, the client sends only when the seller lists `web-bot-auth`. Otherwise it returns `ErrWebBotAuthNotAdvertised` and never downgrades on its own. `identity.AdvertisedRequestSigningProfiles` reads the list from a capabilities response.

3. Serve the key directory on that origin, signed once per key (WBA Appendix B.1). The profile requires the signatures, and verifiers using this SDK refuse a plain JWK Set:

   ```go
   pub, _ := signing.NewDirectoryPublisher(signing.DirectoryPublisherOptions{
       AgentOrigin: "https://buyer.example.com", PrivateKeys: []any{priv, nextKey},
   })
   mux.Handle(signing.DirectoryPath, pub) // re-signs before the signatures expire
   ```

   For static hosting, run `adcp-wba-directory --origin ... --key ... --out-body ... --out-headers ...` again before `--lifetime` elapses. Publish a key before signing with it, because verifiers cache a directory for up to five minutes.

4. Serve a signed revocation list at `/.well-known/governance-revocations.json` on the same origin (`adcp-gov-revocation+jws`, signed by a directory key). **Without one, sellers using this SDK treat your origin as identity-established and won't bind it for spend.** While they can't read it, or it is stale past grace, your spend-committing calls get `SERVICE_UNAVAILABLE`; other calls proceed.
5. List the origin in `https://{your registrable domain}/.well-known/trust.json` with `adcp:buying`. `identity.MarshalTrustRecord(rec, "your-domain.com")` validates it before you publish.

Keep your RFC 9421 setup (`KeyID`, brand.json `jwks_uri`) for sellers that have not moved. The default `Signer` never emits or covers `Signature-Agent`.

## Seller verifier

```go
keys := signing.NewDirectoryResolver(signing.DirectoryResolverOptions{})
revocations, _ := signing.NewOriginRevocationChecker(signing.OriginRevocationCheckerOptions{Keys: keys})
records, _ := identity.NewTrustRecordEvaluator(identity.TrustRecordEvaluatorOptions{PollingInterval: time.Minute})
bindings := identity.NewOriginBindings() // supply a durable identity.BindingStore in production

mw := signing.Middleware(signing.MiddlewareOptions{
    Resolver: rfc9421Resolver,                 // RFC 9421 keeps working through 3.x
    Replay:   sharedReplayStore,               // shared by both profiles
    WebBotAuth: &signing.WebBotAuthOptions{
        Keys:        keys,
        Revocation:  revocations,
        Authorizer:  &identity.BindingGate{Bindings: bindings, Records: records},
        Authorities: []string{"seller.example.com"}, // the host names this endpoint serves
    },
    BoundOrigins: bindings,                    // refuse RFC 9421 for a bound origin
})
// Install mw inside (after) any bearer-token middleware.
```

- **Profile selection.** A request with `Signature-Agent` or a `web-bot-auth` tag is verified under Web Bot Auth only, including under `ObserveOnly`. An uncovered `Signature-Agent` member is never attributed.
- **Body.** A non-empty signed body must be strict JSON: no duplicate keys, including keys that differ only in case. Anything else is `400`.
- **Authorities.** Set `Authorities` so that a signature captured at another seller cannot be replayed to this endpoint under that seller's `Host`.
- **Layers.** Verification (`signing.VerifyWebBotAuthRequest`) needs only the key directory and replay state, so it can run in front of the server. Revocation, bindings, and grants are the authorization layer after it. The middleware runs both.
- **Signature-Agent.** The member value is the signer's origin, spelled exactly as its canonical `https` origin: a lowercase host, no default port, and no trailing slash, path, query, fragment, or userinfo. Any other value, such as `https://buyer.example.com/` or `https://buyer.example.com/agents/a`, is refused with `403` before any directory fetch.
- **One signature.** A request carries exactly one `web-bot-auth` signature and one `Signature-Agent` member. More is refused with `403` and `Accept-Signature`, even when each signature verifies.
- **Required parameters.** `created`, `expires`, `keyid`, `alg`, `nonce`, and `tag`. `alg` is `ed25519` and must match the directory key.
- **Unsigned requests** that need a signature (an operation in `RequiredFor` or a method in `ProtocolMethodsRequiredFor`, read by exact member name, a body carrying `push_notification_config.authentication` under any letter case, or a body that is not strict JSON) get `401` with `request_signature_required` plus `Accept-Signature` for Web Bot Auth, since the middleware also accepts the RFC 9421 profile. A seller that advertises only `web-bot-auth` sets `WebBotAuthOptions.Exclusive`: those requests get `403` with `Accept-Signature`, and every signed request is verified under Web Bot Auth.
- **Interop.**
  - Nonces must be 64 bytes, as unpadded base64url (the encoding this signer emits) or padded standard base64 (which Cloudflare's library and the WBA architecture draft's own vectors use). These are the two forms Cloudflare's `validateNonce` accepts; padded base64url is refused.
  - `@target-uri` is the target URI as received, percent-encoding included (RFC 9421 §2.2.2), so `/a%7Eb/mcp/` is signed and verified as sent. `@authority` is normalized (§2.2.3). The canonical origin is used only for the `Signature-Agent` identity.
  - Keys are Ed25519 only. A directory that also lists an RSA key, or any other key the profile cannot use, skips that key, and the key needs no directory signature.
  - Cross-implementation fixtures from the TypeScript SDK, generated against Cloudflare's `web-bot-auth@0.2.0` and `http-message-sig@0.3.0`, are vendored under `adcp/v3/signing/testdata/wba-interop/` (provenance in `UPSTREAM.json`). The Go verifier accepts both the Cloudflare-signed and the TypeScript-signed requests and their signed directories, percent-encoded path included, and builds the same signature base. With the fixtures' `created` and nonce, the Go signer reproduces the TypeScript signatures byte for byte.
- **Key directories.** Directories must be served as `application/http-message-signatures-directory+json` (WBA §5.5.1), and signed, one signature per usable key (WBA Appendix B.1). A directory with another media type is invalid. An unsigned directory, or one with a key that did not sign it, is a discovery failure.
  - **Discovery failures** (WBA §6.10) cover a directory that doesn't resolve: network or TLS error, timeout, any status other than `200` (including 404, 410, 429, 5xx), an invalid directory, or a fetch refused by the resolver's own limits.
  - A discovery failure never evicts a cached directory. Its keys keep verifying for up to `MaxStale` (24 h by default, never past the directory signatures' expiry). When the refusal comes from the resolver's own limits, they keep verifying for one more 5-minute cache period only.
  - With no usable cached copy, the request is unverified, and what happens is local policy (`WebBotAuthOptions.OnDiscoveryFailure`). The default is `403` with `Accept-Signature`. `DiscoveryFailureAnonymous` serves the request as anonymous, except operations in `RequiredFor`.
  - A directory that resolves without the key is evidence that the key was removed.
- **Errors are split by layer.**
  - **The signature path** answers only with WBA statuses and headers, never `503`: `400` when headers don't parse; `429` with `Accept-Signature` carrying a fresh nonce for a reused signature or nonce; `403` with `Accept-Signature` for every other signature failure; `403` without it for a revoked key. A replay store at its per-identity capacity answers `429` with `Retry-After` and no `Accept-Signature`. Any body is the generic authentication error (`{"errors":[{"code":"AUTH_INVALID",...}]}`, the same for every cause).
  - **Authorization outcomes** after verification use the standard AdCP error envelope (`{"errors":[{"code","message","recovery"}]}`) with existing codes:
    - a failed binding, an unlisted origin, or a signature-required operation from an unbound origin gets `PERMISSION_DENIED` (403) with no details;
    - a spend-committing call while the trust record or the origin's revocation list can't be read gets `SERVICE_UNAVAILABLE` (503, `Retry-After`, retryable);
    - a missing grant gets `PERMISSION_DENIED` with `grant_absent`, from the grant gate.
  - A correctly signed request with a valid key never gets `403` with `Accept-Signature`.
  - `request_signature_*` codes remain for the RFC 9421 profile.
- **Revocation** runs in the authorization layer (`signing.CheckWBARevocation`, from `WebBotAuthOptions.Revocation`) and is recorded in `VerifiedSigner.Revocation`:
  - **revoked:** refused, `403` with no `Accept-Signature`;
  - **no list:** identity-established, and `Bind` refuses it (`identity.ErrRevocationListRequired`), so it can't be bound;
  - **stale** (unreadable, past grace, or not storable in the `RevocationHistory`): spend-committing calls get `SERVICE_UNAVAILABLE` from `BindingGate` and `GrantGate`, and other calls proceed. The last verified list keeps revoking its keys;
  - **unavailable** (the `RevocationHistory` can't be read): every bound call gets `SERVICE_UNAVAILABLE`.
- **Identity-established.** A verified origin with no binding gets what an anonymous caller gets.
  - Operations in `UnboundFor` (default `get_adcp_capabilities` and `get_principal`) run with the verified identity attached. List there whatever needs the identity.
  - Any other operation is served exactly as an anonymous request, with no verified signer, so your application answers it as it answers anonymous callers. An operation in `RequiredFor` or `ProtocolMethodsRequiredFor` is refused with `PERMISSION_DENIED`.
  - The MCP session methods (`initialize`, `notifications/initialized`, `ping`, `tools/list`) and bodyless streamable-HTTP `GET`/`DELETE` are always allowed in addition, so the signer can open and close a session.
  - A2A deployments resolve skills with `OperationResolver`.
  - `get_principal` should return `unconfigured`, with `relationship` (an `identity.PrincipalRelationship`) when an application is in progress.
  - Add `sync_principal` only when its handler implements an explicit `open` policy.
- **Binding.** This is an explicit seller decision: `records.Bind(ctx, signer, principalID, bindings, identity.BindOptions{})`. The operator's trust record must list exactly one agent at the origin with `adcp:buying`.
  - The origin must serve a current revocation list: `Bind` refuses one with no list, a stale list, or no revocation check (`identity.ErrRevocationListRequired`), and `BindingGate` treats such an origin as identity-established.
  - An origin maps to one principal. Binding onto a principal that already uses RFC 9421 needs `ExistingRFC9421Principal: true, MigrationApproved: true`. Set those from your own records: the zero value treats the principal as new, as in the Python SDK.
  - `BindingGate` re-evaluates the trust record on every request, through the evaluator's cache, which is bounded by the polling interval.
    - **Delisted**, or the operator never published a record: the origin falls back to identity-established (anonymous-equivalent, plus `UnboundFor`), not to deny-everything, with no exception (task polling included). The binding stays, so the origin still cannot downgrade to RFC 9421, and `Bind` refuses to bind it again while it is unlisted.
    - **Unreachable or unreadable record**, including a `404`/`410` after the operator published one: a discovery failure, not a delisting and not a removal. An outage never restores access.
      - The last record read stays authoritative. If it didn't list the origin, the origin stays delisted, whatever made the refresh fail, rate and concurrency caps included. Operators retire an agent by publishing an empty `agents[]`.
      - If it did, spend-committing calls (`signing.IsSpendCommittingOperation`, configurable) fail with `SERVICE_UNAVAILABLE`. Other calls may continue under the binding while that record is within `TrustRecordEvaluatorOptions.OutageContinuation` past its freshness. That bound is seller policy (default 4× the polling interval; negative disables). After it, they also get `SERVICE_UNAVAILABLE` until a fetch succeeds.
      - With no record in the authority history, outages are `SERVICE_UNAVAILABLE`.
      - A `404`/`410` from an operator that has never published a record means no record: the origin is not listed. `TrustRecordEvaluator.CheckListing` implements these rules.
    - **Authority history** (which operators have served a trust.json, and the last record read from each) is security state. Losing it turns a later `404` into absence, re-enabling the brand.json fallback, or turns a delisting into an outage. The default `identity.AuthorityHistory` is in memory and does not survive restarts or span replicas. **Public deployments must supply a durable one** (`TrustRecordEvaluatorOptions.History`). If it can't be read or written, the evaluation fails closed as unavailable. The in-memory default refuses rather than evicts: at most 64 operators per quota group and 65,536 overall. A quota group is a registrable domain, and all tenants of a PSL PRIVATE hosting suffix such as `github.io` count as one group. An operator over quota gets no history. No other operator's history is lost, so free subdomains can't flush another operator's history.
- **The trust record is never read on the signature path.** The `signing` package cannot import `identity`, because `identity` imports `signing`.
- **MCP tool handlers.** Read the signer with `signing.VerifiedSignerFromToolRequest(req)`. A stateful go-sdk session binds handlers to the context of its `initialize` request, so `VerifiedSignerFromContext` can return another request's signer there. The middleware places every verified signer, under either profile, in the per-request `auth.TokenInfo`. It sets only `TokenInfo.Extra`, never `UserID`, and the MCP SDK copies it into `req.Extra`. `GrantGate.RequireAccountForToolRequest` uses it.
- **Grants.** Set `adcp.Config.SyncAccountsAuthorizer = gate.SyncAccountsAuthorizer()`, with `gate := &identity.GrantGate{Evaluator: &identity.GrantEvaluator{Records: records}, ExistingAccountClaim: ...}`.
  - The hook reads the per-request signer (Web Bot Auth or RFC 9421) and checks every entry before the handler runs.
  - An identity-established Web Bot Auth signer (no `PrincipalID`) never provisions accounts.
  - A missing grant is `PERMISSION_DENIED` with `{scope: "agent", reason: "grant_absent"}`. When no identity is established, or the signer's own operator doesn't list it, it is `PERMISSION_DENIED` with no details.
  - A grant's `grantee` must be a registrable domain under the pinned PSL snapshot. A grant whose `grantee` is not one covers no claim: a host such as `buy.agency.com` is never widened to `agency.com`, and a public suffix such as `github.io` has no registrable domain (`alice.github.io` is one). Its `scopes` still count for `exclusive_grants`, so ignoring the grant never fails open. The record that carries it stays readable, and the evaluator logs a warning (`TrustRecordEvaluatorOptions.Logger`). `MarshalTrustRecord` refuses such a grantee when `owner` has a registrable domain: write the registrable domain and narrow the grant with `agents[]`. `account.operator`, the brand's domain, `grantee`, and the signer's operator compare as registrable domains.
  - A chain takes one of three shapes:
    - the brand grants the signer's operator, which is `account.operator`;
    - a self-operated brand (it is `account.operator`) grants the signer's operator directly, for example the platform hosting its agent;
    - the brand grants `account.operator`, which grants the signer's operator.
  - Before spend-committing calls, call `gate.RequireAccountForToolRequest(ctx, req, accountID)`. An operation outside media buying needs an explicit scope (`OperationClaim`).
  - A grantor or intermediary record that can't be read is never `grant_absent`. That includes a `404`/`410` after the record was read. The last-read grants stand: a non-spend claim is evaluated on them within `OutageContinuation`. Only a claim marked `NonSpend` continues: `RequireAccountForOperation` and `RequireDeclaration` set it from the operation, using `GrantGate.SpendCommitting`. Any other claim gets `SERVICE_UNAVAILABLE`, which includes an unmarked claim (fail closed) and any claim after the bound. `authorized_operators` doesn't come back: there is no downgrade.
  - `GrantEvaluator.LegacyAuthorization` (brand.json `authorized_operators`) runs only while the brand has never served a trust.json. `SellerAuthorization` is your discretion for uncovered claims and is disabled by `exclusive_grants`.
- **Revocation.** `OriginRevocationChecker` retains every thumbprint-shaped revoked kid, at most 1024 per origin. It keeps `keyCompromise` and the earliest `invalidity_date`, and refuses older lists. Revocations survive eviction of an origin through `RevocationHistory`. The default history is in memory and refuses rather than evicts: at most 32 origins and 8,192 kids per quota group (the same grouping as the authority history), plus global bounds. An origin that doesn't fit is `RevocationStale` (no spend), and no other origin's revocations are forgotten. A stale list still revokes everything it revoked. **Public deployments must supply a durable `RevocationHistory`**, so revocations hold across restarts and replicas. `RevocationDetails.Exposes(receivedAt)` tells you which accepted buys came from suspect requests; never unwind them automatically.
- **Advertising.** Advertise the profile with `identity.RequestSigningCapabilities{Profiles: []string{signing.ProfileTagRequestSigning, signing.ProfileTagWebBotAuth}}`, and advertise onboarding with `identity.AccountCapabilities.AgentOnboarding` (list `account.agent_onboarding` in `experimental_features`).

## Deprecation

AdCP 3.3 deprecates the RFC 9421 profile. It stays fully functional through 3.x and is removed in 4.0. `signing.Middleware` logs this once per process when `WebBotAuth` is not set. The RFC 9421 APIs are not marked `Deprecated:` in Go, so `staticcheck` builds keep passing while 3.x still requires them. JWS document signing (governance context, response signing, rights attestations) stays on the RFC 9421 chain until 3.4.

## Rollback

- **Signers:** clear `AgentOrigin` and set `KeyID` to your brand.json kid to sign with RFC 9421 again. A seller that has bound your origin refuses that until it unbinds.
- **Sellers:** remove `WebBotAuth`. Bindings remain in your store and still block RFC 9421 for those origins through `BoundOrigins`, so unbind them first.

## Not covered yet

- **Legacy module.** These features ship in `adcp/v3` only. The legacy `adcp` module is frozen at v2 (see MIGRATING.md) and received only the covered-header tolerance.
- **Webhooks.** Web Bot Auth webhooks are ordinary signed requests and verify with the same middleware. The `adcp/v3/webhook` publisher does not sign them yet, and the `signing_profile` registration field is not modeled.
- **Durable state.** The defaults keep bindings, revocation history, authority history, and replay state in memory. Persistent stores are the application's, behind `BindingStore`, `RevocationHistory`, `AuthorityHistory`, and `ReplayStore`. Public deployments need all four.
- **Released inputs.** The final 3.3 schema bundle and the P7 trust-record and grant-chain vectors are upstream release dependencies. Re-pin and rerun conformance when they land.
