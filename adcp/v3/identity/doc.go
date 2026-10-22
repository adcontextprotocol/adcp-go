// Package identity is the EXPERIMENTAL AdCP 3.3 agent-identity layer above
// request verification: trust.json evaluation, origin-to-principal binding,
// demand-side grants (DR-0025), and Go types for the identity schemas that
// are not yet in a released schema bundle (onboarding posture, principal
// relationship, grant_absent, trust.json v1 without keys).
//
// The design is specs/agent-identity-3-3.md and DR-0023/DR-0025 in
// adcontextprotocol/adcp. The schemas are pinned to draft commits recorded
// in testdata/schemas/UPSTREAM.json and may change before 3.3 ships. The
// SDK's wire pin is unchanged; nothing here is used unless configured.
//
// # Where the trust record is read
//
// signing verifies a Web Bot Auth signature and yields a verified origin. It
// never reads a trust record, and this package is not imported by signing,
// so the signature path cannot consult one. A trust record is evaluated only
// when something is gated on it: binding a principal (BindingGate,
// TrustRecordEvaluator.Bind), evaluating a grant (GrantEvaluator), and open
// onboarding (TrustRecordEvaluator.Evaluate with the required role). For a
// JWS artifact, JWSVerifier derives the record from the issuer's origin to
// select the profile (spec R4); the request-signature path still never reads
// one.
//
// # Authority
//
// A verified origin with no binding is identity-established. The spec gives
// it what an anonymous caller gets, reads included, plus get_principal; the
// signing middleware allows it only WebBotAuthOptions.UnboundFor, which a
// seller sets to exactly that. A binding is an explicit seller decision, made only
// after the operator's trust record lists the origin with the required role,
// re-evaluated at least as often as the revocation polling interval, and it
// maps an origin to one principal. Binding onto a principal that already
// uses the RFC 9421 profile needs explicit approval, and a bound origin is
// refused the RFC 9421 profile (signing.MiddlewareOptions.BoundOrigins). An
// origin must serve a current revocation list to be bound; one that serves
// none stays identity-established.
//
// # Outages
//
// A trust record that cannot be read, including a 404 or 410 after the
// operator published one, is a discovery failure: never absence, never a
// delisting, and never a reason to fall back to brand.json. The last record
// read stays authoritative; operators retire an agent by publishing an empty
// agents[]. Spend-committing calls fail with SERVICE_UNAVAILABLE during an
// outage, as they do while the signer's revocation list is stale. Other calls
// may continue on the last record within TrustRecordEvaluatorOptions.
// OutageContinuation, which is seller policy (4x the polling interval by
// default).
package identity
