package signing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/strictjson"
)

// EXPERIMENTAL: the Web Bot Auth (WBA) profile is experimental in AdCP 3.3
// (security.mdx, Web Bot Auth identities; specs/agent-identity-3-3.md). Its
// wire rules follow the IETF Web Bot Auth draft and the AdCP profile page
// (docs/building/by-layer/L1/wba-profile.mdx, adcontextprotocol/adcp#7894),
// which may still change. Everything here is opt-in: no existing signer or
// verifier path uses it unless configured to.
//
// Verification establishes an identity, never authority. It needs only the
// signer's key directory and replay state (VerifyWebBotAuthRequest), so it
// can run in front of the server. Revocation, bindings, and grants are the
// authorization layer that runs after it. A verified origin the seller has
// not bound is identity-established: Middleware allows it only
// WebBotAuthOptions.UnboundFor, which a seller sets to its anonymous
// permissions plus get_principal. Nothing on the signature path reads a
// trust record or a revocation list.

const (
	// ProfileTagWebBotAuth is the Signature-Input tag, and the
	// request_signing.profiles identifier, of the Web Bot Auth profile.
	ProfileTagWebBotAuth = "web-bot-auth"

	// ProfileTagRequestSigning is the request_signing.profiles identifier
	// of the AdCP RFC 9421 profile.
	ProfileTagRequestSigning = "adcp/request-signing/v1"

	// DirectoryTag is the tag on a key directory response signature (WBA
	// Appendix B.1).
	DirectoryTag = "http-message-signatures-directory"

	// DirectoryPath is the well-known path of an origin's key directory.
	DirectoryPath = "/.well-known/http-message-signatures-directory"

	// DirectoryMediaType is the key directory's media type.
	DirectoryMediaType = "application/http-message-signatures-directory+json"

	signatureAgentHeader  = "Signature-Agent"
	acceptSignatureHeader = "Accept-Signature"

	wbaMaxWindowSeconds int64 = 300
	wbaNonceBytes       int   = 64 // web bot auth architecture draft §4.2.2
	wbaThumbprintLen    int   = 43 // SHA-256 as unpadded base64url
)

// validWBANonce reports whether nonce encodes exactly 64 bytes, canonically,
// as unpadded base64url (RFC 4648 §5) or padded standard base64 (§4): the
// two forms Cloudflare's web-bot-auth validateNonce accepts. This package's
// signer emits unpadded base64url. Padded standard base64 is what that
// library's generateNonce and the Web Bot Auth architecture draft's
// Appendix A nonces use. Padded base64url, and a nonce of any other length,
// is refused.
func validWBANonce(nonce string) bool {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding} {
		b, err := enc.DecodeString(nonce)
		if err == nil && len(b) == wbaNonceBytes && enc.EncodeToString(b) == nonce {
			return true
		}
	}
	return false
}

// WBAError is a Web Bot Auth failure. On the signature path it is the HTTP
// status the profile defines (WBA §5.3 and §5.4): 400 when the signature
// fields do not parse; 429 with Accept-Signature, carrying a fresh nonce,
// when the signature or its nonce was used before (Replayed); and 403 with
// Accept-Signature when a signature, a profile check, or key discovery
// fails, or without it for a revoked key. A replay store at its per-identity
// capacity is 429 with Retry-After and no Accept-Signature. Any body is the
// generic authentication error.
//
// DiscoveryFailure marks a signature that could not be verified because the
// signer's key directory could not be resolved (WBA §6.10: a network or TLS
// error, a timeout, any status other than 200, or a response that is not a
// valid directory). The request is unverified, which says nothing about the
// signer; Middleware applies WebBotAuthOptions.OnDiscoveryFailure.
//
// Revoked marks a key the signer's origin has revoked: 403 without
// Accept-Signature, so the signer does not sign again with the same key.
//
// Authorization is set for an authorization-layer outcome once identity is
// established. It is answered with the standard AdCP error envelope, never
// 403 with Accept-Signature, which would tell a correctly signing agent to
// sign again. Code is its AdCP error code (PERMISSION_DENIED when empty, or
// SERVICE_UNAVAILABLE for a retryable outage).
//
// Reason is for server-side logs only and must never be sent to the caller.
type WBAError struct {
	Status           int
	Reason           string
	Label            string
	Authorization    bool
	Code             string
	DiscoveryFailure bool
	Revoked          bool
	// Replayed marks a reused signature or nonce: 429 with Accept-Signature
	// carrying a fresh nonce.
	Replayed bool
	Wrapped  error

	// noBody drops content-type and content-digest from Accept-Signature
	// for a request without a body.
	noBody bool
}

func (e *WBAError) Error() string {
	return fmt.Sprintf("web bot auth %d: %s", e.Status, e.Reason)
}

func (e *WBAError) Unwrap() error { return e.Wrapped }

// AsWBAError returns err as a *WBAError, or nil.
func AsWBAError(err error) *WBAError {
	var e *WBAError
	if errors.As(err, &e) {
		return e
	}
	return nil
}

func wbaFail(status int, label, reason string) *WBAError {
	return &WBAError{Status: status, Label: label, Reason: reason}
}

func wbaReplayed(label string) *WBAError {
	return &WBAError{Status: http.StatusTooManyRequests, Label: label, Reason: "replayed nonce", Replayed: true}
}

func wbaDeny(reason string) *WBAError {
	return &WBAError{Status: http.StatusForbidden, Reason: reason, Authorization: true, Code: CodePermissionDenied}
}

// ErrDiscoveryFailure is matched (errors.Is) by a *WBAError whose signature
// could not be verified because the signer's key directory could not be
// resolved and no unexpired cached copy holds the key (WBA §6.10).
var ErrDiscoveryFailure = errors.New("signing: signer key directory discovery failed")

// IsDiscoveryFailure reports whether err is a key discovery failure.
func IsDiscoveryFailure(err error) bool { return errors.Is(err, ErrDiscoveryFailure) }

func discoveryFailure(reason string, wrapped error) *WBAError {
	return &WBAError{Status: http.StatusForbidden, Reason: reason, Wrapped: wrapped, DiscoveryFailure: true}
}

// Is reports a discovery failure as ErrDiscoveryFailure.
func (e *WBAError) Is(target error) bool { return target == ErrDiscoveryFailure && e.DiscoveryFailure }

// AdCP error codes used for authorization outcomes after identity is
// established.
const (
	CodePermissionDenied   = "PERMISSION_DENIED"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// NewAuthorizationError returns the error a WBAAuthorizer returns for an
// established identity: code is an AdCP error code. PERMISSION_DENIED is
// answered with HTTP 403 and SERVICE_UNAVAILABLE (retryable) with 503, both
// in the AdCP error envelope and without Accept-Signature. reason is for
// logs only.
func NewAuthorizationError(code, reason string, wrapped error) *WBAError {
	status := http.StatusForbidden
	if code == CodeServiceUnavailable {
		status = http.StatusServiceUnavailable
	}
	return &WBAError{Status: status, Reason: reason, Authorization: true, Code: code, Wrapped: wrapped}
}

// spendCommittingOperations commit spend (security.mdx's spend-committing
// row), plus the 3.2 buy and proposal-acceptance tools.
var spendCommittingOperations = map[string]struct{}{
	"create_media_buy": {}, "update_media_buy": {}, "buy_products": {}, "accept_proposal": {}, "activate_signal": {},
}

// IsSpendCommittingOperation reports whether op commits spend: the
// operations a trust-record or revocation outage fails closed for in the
// authorization layer. It
// covers create_media_buy, update_media_buy, buy_products, accept_proposal,
// activate_signal, and every acquire_* operation.
func IsSpendCommittingOperation(op string) bool {
	if _, ok := spendCommittingOperations[op]; ok {
		return true
	}
	return strings.HasPrefix(op, "acquire_")
}

// ResponseHeader returns the headers to send with the failure. A 403
// signature failure other than a revoked key carries Accept-Signature (RFC
// 9421 §5.1) naming the label, components, and parameters the profile
// requires. A replay (429) carries it too, with a fresh nonce the signer
// uses on its next attempt (WBA §5.3). A 429 for replay-store capacity
// carries Retry-After instead.
func (e *WBAError) ResponseHeader() http.Header {
	h := http.Header{}
	if e.Authorization || e.Revoked {
		return h
	}
	switch {
	case e.Status == http.StatusForbidden:
		h.Set(acceptSignatureHeader, acceptSignatureValue(e.acceptLabel(), !e.noBody, ""))
	case e.Status == http.StatusTooManyRequests && e.Replayed:
		h.Set(acceptSignatureHeader, acceptSignatureValue(e.acceptLabel(), !e.noBody, newWBANonce()))
	case e.Status == http.StatusTooManyRequests:
		h.Set("Retry-After", "60")
	}
	return h
}

func (e *WBAError) acceptLabel() string {
	if e.Label == "" || !isValidLabel(e.Label) {
		return "sig1"
	}
	return e.Label
}

// acceptSignatureValue is the Accept-Signature member the profile requires
// (RFC 9421 §5.1): the components every request covers, plus content-type
// and content-digest for a request with a body, and the parameters
// created, expires, nonce, keyid, alg, and tag="web-bot-auth". A non-empty
// nonce is offered as the value to sign with next; otherwise nonce is only
// listed.
func acceptSignatureValue(label string, body bool, nonce string) string {
	components := []sfItem{
		{value: sfStringItem(componentMethod)},
		{value: sfStringItem(componentTargetURI)},
		{value: sfStringItem(componentAuthority)},
	}
	if body {
		components = append(components, sfItem{value: sfStringItem(componentContentType)}, sfItem{value: sfStringItem(componentContentDigst)})
	}
	components = append(components, sfItem{value: sfStringItem("signature-agent"), params: []sfParam{{key: "key", value: sfStringItem(label)}}})
	nonceParam := sfTrue()
	if nonce != "" {
		nonceParam = sfStringItem(nonce)
	}
	return sfDict{{key: label, member: sfMember{inner: true, list: components, params: []sfParam{
		{key: "created", value: sfTrue()},
		{key: "expires", value: sfTrue()},
		{key: "nonce", value: nonceParam},
		{key: "keyid", value: sfTrue()},
		{key: "alg", value: sfTrue()},
		{key: "tag", value: sfStringItem(ProfileTagWebBotAuth)},
	}}}}.serialize()
}

// AcceptSignature returns the Accept-Signature value (RFC 9421 §5.1) the
// Web Bot Auth profile asks a signer for, under label "sig1": the
// components and parameters it requires, with content-type and
// content-digest when the request has a body. A verifier sends it with an
// unsigned request it refuses (Middleware does).
func AcceptSignature(body bool) string { return acceptSignatureValue("sig1", body, "") }

func newWBANonce() string {
	var b [wbaNonceBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return b64UrlEncodeRaw(b[:])
}

// UsesWebBotAuth reports whether a signed message selects the Web Bot Auth
// profile: it carries a Signature-Agent field, or a Signature-Input member
// tagged web-bot-auth. Such a message is verified under that profile only;
// a verifier that has enabled Web Bot Auth never falls back to the RFC 9421
// profile for it. A Signature-Input that does not parse still selects the
// profile when it mentions the tag, so a malformed marker cannot force a
// downgrade.
func UsesWebBotAuth(h http.Header) bool {
	if len(h.Values(signatureAgentHeader)) > 0 {
		return true
	}
	raw := strings.Join(h.Values(signatureInputHeader), ", ")
	if raw == "" {
		return false
	}
	d, err := parseSFDictionary(raw)
	if err != nil {
		return strings.Contains(raw, ProfileTagWebBotAuth)
	}
	for _, e := range d {
		if tag, ok := stringParam(e.member.params, "tag"); ok && tag == ProfileTagWebBotAuth {
			return true
		}
	}
	return false
}

// WBAKeyResolver resolves a Web Bot Auth key by the pair (origin, key ID),
// never by the key ID alone (WBA §5.4): the origin is the one the covered
// Signature-Agent member names. It returns (nil, nil) when the origin's
// directory does not hold the key. A resolver that cannot resolve the
// directory returns a *WBAError with Status 403 and DiscoveryFailure set,
// so the verifier reports ErrDiscoveryFailure. It is the resolver for many signers that
// a seller with many buyers uses instead of a hand-written keyid-to-agent
// map; DirectoryResolver is the production implementation.
type WBAKeyResolver interface {
	ResolveWBAKey(ctx context.Context, origin, keyID string) (*JWK, error)
}

// RevocationState is a verified key's status in its origin's revocation
// list, established in the authorization layer after verification.
type RevocationState int

const (
	// RevocationUnchecked: no revocation checker ran.
	RevocationUnchecked RevocationState = iota
	// RevocationCurrent: a list within its freshness (or grace) does not
	// revoke the key.
	RevocationCurrent
	// RevocationRevoked: the origin revoked the key. The request is
	// refused with 403, without Accept-Signature.
	RevocationRevoked
	// RevocationNoList: the origin serves no revocation list. The identity
	// stays identity-established: it cannot be bound, and a binding gives
	// it no principal.
	RevocationNoList
	// RevocationStale: the list could not be read, or is past its
	// next_update plus grace. The identity is not established for spend:
	// spend-committing calls fail closed with SERVICE_UNAVAILABLE in the
	// authorization layer. Other calls proceed.
	RevocationStale
	// RevocationUnavailable: the revocations retained for the origin
	// cannot be read (a revocation-store failure), so none of its calls
	// may act under a binding: the authorization layer answers
	// SERVICE_UNAVAILABLE.
	RevocationUnavailable
)

// String returns the state's name, for logs.
func (s RevocationState) String() string {
	switch s {
	case RevocationCurrent:
		return "current"
	case RevocationRevoked:
		return "revoked"
	case RevocationNoList:
		return "no_list"
	case RevocationStale:
		return "stale"
	case RevocationUnavailable:
		return "unavailable"
	default:
		return "unchecked"
	}
}

// WBARevocationChecker reports a key's state in origin's signed revocation
// list (/.well-known/governance-revocations.json). The error, when non-nil,
// explains a RevocationStale or RevocationNoList result for logs. It runs in
// the authorization layer, after verification. OriginRevocationChecker is
// the production implementation.
type WBARevocationChecker interface {
	CheckWBAKey(ctx context.Context, origin, keyID string) (RevocationState, error)
}

// CheckWBARevocation runs checker for the verified signer and records the
// result in VerifiedSigner.Revocation. A revoked key returns a *WBAError
// with Revoked set: 403 with the generic authentication error and no
// Accept-Signature, because the signer must stop using that key.
func CheckWBARevocation(ctx context.Context, checker WBARevocationChecker, signer *VerifiedSigner) error {
	state, err := checker.CheckWBAKey(ctx, signer.AgentURL, signer.KeyID)
	signer.Revocation = state
	if state == RevocationRevoked {
		return &WBAError{Status: http.StatusForbidden, Label: signer.Label, Reason: "signing key revoked", Revoked: true, Wrapped: err}
	}
	return nil
}

// WBAVerifyOptions configures VerifyWebBotAuthRequest. Keys and Replay are
// required.
type WBAVerifyOptions struct {
	// Keys resolves (origin, thumbprint) to a directory key.
	Keys WBAKeyResolver

	// Replay records each signature's nonce per identity. The identity, not
	// the key, scopes the nonce, so key rotation does not reopen a replay.
	// A store that implements ReplayClaimer (MemoryReplayStore does) records
	// atomically, so concurrent copies of one request cannot both pass.
	Replay ReplayStore

	// Clock supplies the verifier's wall clock. Defaults to time.Now.
	Clock func() time.Time

	// Scheme is the request URL scheme when the request is not absolute.
	// Defaults to "https".
	Scheme string

	// MaxBodyBytes caps the body read for the digest check. 0 selects
	// 32 MiB.
	MaxBodyBytes int64

	// Authorities, when set, lists the authorities (host[:port]) this
	// verifier serves. A request whose @authority is not among them is
	// rejected, so a signature captured at one seller cannot be replayed to
	// another seller that accepts any Host. Recommended for any verifier
	// reachable under more than one name or behind a default backend.
	Authorities []string
}

// errWBANotConfigured reports a verifier missing a required dependency.
var errWBANotConfigured = errors.New("signing: Web Bot Auth verification requires Keys and Replay")

// VerifyWebBotAuthRequest verifies the request's one web-bot-auth signature
// and returns the verified signer. Its AgentURL is the signer's canonical
// origin and its KeyID is the key's thumbprint.
//
// The profile takes exactly one web-bot-auth signature and exactly one
// Signature-Agent member per request. A request that carries more of
// either is refused with 403 and Accept-Signature, even when each signature
// verifies on its own: an intermediary that signs a request is its signer,
// and whether it may act for another party is an authorization question,
// not a second signature. Signature-Input members with another tag are
// not read, but every member of both fields must be well formed (RFC 9421
// §4.1 and §4.2): a malformed member under any label is 400.
//
// A failure is a *WBAError carrying the status to answer: 400 when the
// signature fields do not parse, 429 with Accept-Signature and a fresh nonce
// for a reused signature or nonce, and 403 with Accept-Signature for every
// other signature failure. A signature whose key directory cannot be
// resolved matches ErrDiscoveryFailure: the request is unverified, and what
// to do with it is local policy (WBA §6.10). The function verifies identity
// only, from the key directory and replay state: it reads no revocation
// list and no trust record, and grants no authority. Run CheckWBARevocation,
// then bindings and grants, after it.
//
// A non-empty signed body must be strict JSON (AdCP bodies are JSON-RPC
// or JSON): one value, no duplicate keys, including keys that differ only
// in case, so every reader sees the same members. Anything else is 400.
func VerifyWebBotAuthRequest(r *http.Request, opts WBAVerifyOptions) (*VerifiedSigner, error) {
	signer, err := verifyWebBotAuthRequest(r, opts)
	if we := AsWBAError(err); we != nil && !we.Authorization {
		// Accept-Signature lists content-type and content-digest only for a
		// request with a body, as the profile requires them only there.
		we.noBody = !requestHasBody(r, opts.MaxBodyBytes)
	}
	return signer, err
}

// requestHasBody reports whether r carries a non-empty body, buffering it
// for later readers. A body that cannot be read counts as present.
func requestHasBody(r *http.Request, limit int64) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	b, err := readAndReplaceBody(r, limit)
	return err != nil || len(b) > 0
}

func verifyWebBotAuthRequest(r *http.Request, opts WBAVerifyOptions) (*VerifiedSigner, error) {
	if opts.Keys == nil || opts.Replay == nil {
		return nil, errWBANotConfigured
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	now := clock().Unix()
	scheme := opts.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if err := wbaHeaderPrecheck(r.Header); err != nil {
		return nil, err
	}
	inputs, err := parseSFDictionary(r.Header.Get(signatureInputHeader))
	if err != nil {
		return nil, wbaFail(http.StatusBadRequest, "", "Signature-Input does not parse")
	}
	var (
		label  string
		member sfMember
		count  int
	)
	for _, e := range inputs {
		// RFC 9421 §4.1: every Signature-Input member is an inner list, so
		// a member that is not makes the field malformed, 400, whatever its
		// label or tag (profile verification step 1). This runs before the
		// web-bot-auth member is chosen, so a malformed web-bot-auth member
		// is 400, not "no web-bot-auth signature". The Signature half of the
		// check runs after the 403 below: an AdCP 3.0 or 3.1 signer's
		// unpadded base64url Signature is often not an RFC 8941 Byte
		// Sequence, and it needs the 403 and Accept-Signature hint to
		// switch profiles.
		if !e.member.inner {
			return nil, wbaFail(http.StatusBadRequest, "", "Signature-Input member is malformed")
		}
		if tag, ok := stringParam(e.member.params, "tag"); ok && tag == ProfileTagWebBotAuth {
			if count == 0 {
				label, member = e.key, e.member
			}
			count++
		}
	}
	// A request with no web-bot-auth member (an RFC 9421 signature, say)
	// carries nothing this profile reads: 403 with Accept-Signature, decided
	// before Signature is parsed, so the answer does not depend on whether
	// another profile's Signature value happens to be valid RFC 8941.
	if count == 0 {
		return nil, wbaFail(http.StatusForbidden, "", "no web-bot-auth signature")
	}
	signatures, err := parseSFDictionary(r.Header.Get(signatureHeader))
	if err != nil {
		return nil, wbaFail(http.StatusBadRequest, "", "Signature does not parse")
	}
	// A missing Signature-Agent (selection by tag alone) leaves the member
	// absent, which fails the signature with 403 below; a present field
	// that does not parse is 400.
	var agents sfDict
	if raw := r.Header.Get(signatureAgentHeader); raw != "" {
		if agents, err = parseSFDictionary(raw); err != nil {
			return nil, wbaFail(http.StatusBadRequest, "", "Signature-Agent does not parse")
		}
	}
	if !sameKeys(inputs.keys(), signatures.keys()) {
		return nil, wbaFail(http.StatusBadRequest, "", "Signature and Signature-Input labels differ")
	}
	if !wbaFieldsWellFormed(inputs, signatures) {
		return nil, wbaFail(http.StatusBadRequest, "", "Signature or Signature-Input member is malformed")
	}
	if count > 1 {
		return nil, wbaFail(http.StatusForbidden, label, "more than one web-bot-auth signature")
	}
	if len(agents) > 1 {
		return nil, wbaFail(http.StatusForbidden, label, "more than one Signature-Agent member")
	}

	limit := opts.MaxBodyBytes
	if limit <= 0 {
		limit = defaultMaxBodyBytes
	}
	body, err := readAndReplaceBody(r, limit)
	if err != nil {
		return nil, wbaFail(http.StatusForbidden, label, "request body unreadable or too large")
	}
	requestURL, ok := receivedTargetURI(r, scheme)
	if !ok {
		return nil, wbaFail(http.StatusForbidden, label, "absolute-form request-target does not match the connection's scheme and Host")
	}
	if len(opts.Authorities) > 0 {
		u, err := url.Parse(requestURL)
		if err != nil || !authorityAllowed(canonicalAuthority(u.Host, strings.ToLower(u.Scheme)), strings.ToLower(u.Scheme), opts.Authorities) {
			return nil, wbaFail(http.StatusForbidden, label, "request authority is not served by this verifier")
		}
	}

	if err := wbaWindow(member, now, false, label); err != nil {
		return nil, err
	}
	nonce, ok := stringParam(member.params, "nonce")
	if !ok || !validWBANonce(nonce) {
		return nil, wbaFail(http.StatusForbidden, label, "missing nonce, or not 64 bytes in canonical base64")
	}
	// alg is required and names the profile's one algorithm; it must also
	// match the directory key (wbaVerifyCrypto).
	if alg, ok := stringParam(member.params, "alg"); !ok || Algorithm(alg) != AlgEd25519 {
		return nil, wbaFail(http.StatusForbidden, label, "alg must be ed25519")
	}
	agent, ok := agents.get(label)
	if !ok || agent.inner || agent.item.value.kind != sfString {
		return nil, wbaFail(http.StatusForbidden, label, "missing supported Signature-Agent member")
	}
	if typ, present := sfParamValue(agent.item.params, "type"); present && (typ.kind != sfToken && typ.kind != sfString || typ.s != "directory") {
		return nil, wbaFail(http.StatusForbidden, label, "unsupported Signature-Agent member type")
	}
	origin, err := signatureAgentOrigin(agent.item.value.s)
	if err != nil {
		return nil, &WBAError{Status: http.StatusForbidden, Label: label, Reason: "invalid signer origin", Wrapped: err}
	}

	covered := make(map[string]struct{}, len(member.list))
	names := make([]string, 0, len(member.list))
	for _, c := range member.list {
		id := c.serialize()
		covered[id] = struct{}{}
		names = append(names, id)
	}
	required := []string{`"@method"`, `"@target-uri"`, `"@authority"`, `"signature-agent";key=` + sfStringItem(label).serialize()}
	if len(body) > 0 {
		required = append(required, `"content-type"`, `"content-digest"`)
	}
	if len(covered) != len(member.list) {
		return nil, wbaFail(http.StatusForbidden, label, "repeated covered component")
	}
	for _, id := range required {
		if _, ok := covered[id]; !ok {
			return nil, wbaFail(http.StatusForbidden, label, "missing covered component")
		}
	}

	keyID, _ := stringParam(member.params, "keyid")
	jwk, err := opts.Keys.ResolveWBAKey(r.Context(), origin, keyID)
	if err != nil {
		if we := AsWBAError(err); we != nil {
			if we.Label == "" {
				we.Label = label
			}
			return nil, we
		}
		return nil, &WBAError{Status: http.StatusForbidden, Label: label, Reason: "signer key resolution failed", Wrapped: err}
	}
	if jwk == nil {
		return nil, wbaFail(http.StatusForbidden, label, "key absent from the named origin's directory")
	}
	if tp, err := JWKThumbprint(*jwk); err != nil || tp != keyID {
		return nil, wbaFail(http.StatusForbidden, label, "directory key thumbprint does not match keyid")
	}
	base, err := wbaSignatureBase(r.Method, requestURL, r.Header, member, false)
	if err != nil {
		return nil, withLabel(err, label)
	}
	sigMember, _ := signatures.get(label)
	alg, err := wbaVerifyCrypto(member, sigMember, *jwk, base)
	if err != nil {
		return nil, withLabel(err, label)
	}
	if len(body) > 0 && !wbaDigestMatches(r.Header.Get(contentDigestHeader), body) {
		return nil, wbaFail(http.StatusForbidden, label, "request digest mismatch")
	}

	// Replay: the identity, not the key, scopes the nonce. A reused
	// signature or nonce is 429 with Accept-Signature carrying a fresh
	// nonce (WBA §5.3).
	expires, _ := intParam(member.params, "expires")
	// One extra second keeps the entry alive through the last second of
	// the window.
	ttl := time.Duration(expires-now+skewSeconds+1) * time.Second
	if ttl < time.Second {
		ttl = time.Second
	}
	id := wbaReplayIdentity(origin)
	if opts.Replay.Seen(id, nonce) {
		return nil, wbaReplayed(label)
	}
	if opts.Replay.HitCap(id) {
		return nil, wbaFail(http.StatusTooManyRequests, label, "replay store capacity exceeded")
	}
	if claimer, ok := opts.Replay.(ReplayClaimer); ok {
		switch claimer.Claim(id, nonce, ttl) {
		case ReplayReplayed:
			return nil, wbaReplayed(label)
		case ReplayCapacity:
			return nil, wbaFail(http.StatusTooManyRequests, label, "replay store capacity exceeded")
		}
	} else if !opts.Replay.Insert(id, nonce, ttl) {
		return nil, wbaFail(http.StatusTooManyRequests, label, "replay store capacity exceeded")
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := strictjson.Validate(body, 0); err != nil {
			return nil, wbaFail(http.StatusBadRequest, "", "malformed signed JSON body")
		}
	}
	return &VerifiedSigner{
		KeyID:             keyID,
		AgentURL:          origin,
		VerifiedAt:        time.Unix(now, 0).UTC(),
		Algorithm:         alg,
		Label:             label,
		CoveredComponents: names,
		Profile:           ProfileTagWebBotAuth,
	}, nil
}

// receivedTargetURI returns the request's target URI as received (RFC 9421
// §2.2.2): the scheme, the Host field, and the request-target exactly as it
// arrived, so percent-encoding is preserved. An absolute-form
// request-target is used as is, and only when its scheme is the
// connection's and its authority is the Host field's (ok is false
// otherwise), so a signature captured over one scheme cannot be replayed
// over another.
func receivedTargetURI(r *http.Request, scheme string) (target string, ok bool) {
	raw := r.RequestURI
	switch {
	case strings.HasPrefix(raw, "/"):
		return scheme + "://" + r.Host + raw, true
	case raw != "" && raw != "*":
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() {
			return "", false
		}
		if !strings.EqualFold(u.Scheme, scheme) || canonicalAuthority(u.Host, scheme) != canonicalAuthority(r.Host, scheme) {
			return "", false
		}
		return raw, true
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if r.URL.IsAbs() {
		scheme = r.URL.Scheme
	}
	return scheme + "://" + host + r.URL.RequestURI(), true
}

// sentTargetURI returns the target URI a client request will carry on the
// wire: Go writes the request-target as URL.RequestURI() and the Host field
// from Request.Host, else URL.Host.
func sentTargetURI(r *http.Request) string {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	return r.URL.Scheme + "://" + host + r.URL.RequestURI()
}

func authorityAllowed(authority, scheme string, allowed []string) bool {
	for _, a := range allowed {
		if canonicalAuthority(a, scheme) == authority {
			return true
		}
	}
	return false
}

// wbaReplayIdentity scopes a nonce to an identity in a ReplayStore shared
// with RFC 9421 keyids. The separator is the ASCII unit separator (0x1F), a
// control character no HTTP field value can carry (net/http refuses it), so
// no RFC 9421 keyid can name a Web Bot Auth identity and exhaust its
// replay-cache capacity.
func wbaReplayIdentity(origin string) string { return "web-bot-auth\x1f" + origin }

func withLabel(err error, label string) error {
	if we := AsWBAError(err); we != nil && we.Label == "" {
		we.Label = label
	}
	return err
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, k := range a {
		set[k] = struct{}{}
	}
	for _, k := range b {
		if _, ok := set[k]; !ok {
			return false
		}
	}
	return true
}

// wbaFieldsWellFormed reports whether every Signature-Input member is an
// inner list and every Signature member an unparameterized byte sequence
// (RFC 9421 §4.1 and §4.2), whatever its label. Cloudflare's
// http-message-sig refuses a field that breaks either rule as malformed, so
// a malformed member with another tag makes the field malformed too, though
// nothing else is read from that member.
func wbaFieldsWellFormed(inputs, signatures sfDict) bool {
	for _, e := range inputs {
		if !e.member.inner {
			return false
		}
	}
	for _, e := range signatures {
		if e.member.inner || e.member.item.value.kind != sfBinary || len(e.member.item.params) != 0 {
			return false
		}
	}
	return true
}

// wbaHeaderPrecheck rejects ambiguous signature fields before parsing (400):
// a signed field on more than one header line, and a comma-joined
// Content-Type.
func wbaHeaderPrecheck(h http.Header) error {
	for _, name := range []string{signatureHeader, signatureInputHeader, signatureAgentHeader, contentTypeHeader, contentDigestHeader} {
		if len(h.Values(name)) > 1 {
			return wbaFail(http.StatusBadRequest, "", name+" arrived on more than one header line")
		}
	}
	if containsTopLevelComma(h.Get(contentTypeHeader)) {
		return wbaFail(http.StatusBadRequest, "", "Content-Type carries more than one value")
	}
	if h.Get(signatureInputHeader) == "" || h.Get(signatureHeader) == "" {
		return wbaFail(http.StatusBadRequest, "", "Signature and Signature-Input are required")
	}
	return nil
}

// wbaWindow checks the signature parameters' types and validity window.
// Request signatures allow 60 s of skew and at most 300 s of lifetime;
// directory response signatures allow no skew and any lifetime.
func wbaWindow(member sfMember, now int64, directory bool, label string) error {
	if _, ok := stringParam(member.params, "tag"); !ok {
		return wbaFail(http.StatusForbidden, label, "signature tag must be an sf-string")
	}
	created, ok1 := intParam(member.params, "created")
	expires, ok2 := intParam(member.params, "expires")
	if !ok1 || !ok2 {
		return wbaFail(http.StatusForbidden, label, "missing integer time parameter")
	}
	if expires <= created || (!directory && expires-created > wbaMaxWindowSeconds) {
		return wbaFail(http.StatusForbidden, label, "invalid signature lifetime")
	}
	skew := skewSeconds
	if directory {
		skew = 0
	}
	if created > now+skew || expires < now-skew {
		return wbaFail(http.StatusForbidden, label, "signature outside its validity window")
	}
	if keyID, ok := stringParam(member.params, "keyid"); !ok || len(keyID) != wbaThumbprintLen {
		return wbaFail(http.StatusForbidden, label, "missing thumbprint keyid")
	}
	return nil
}

// wbaSignatureBase builds the RFC 9421 §2.5 signature base for member.
// rawURL is the target URI as sent or received, and @target-uri is that
// string unchanged; @authority is its normalized authority. A field
// component is the field's value, or with a key parameter
// the re-serialized Dictionary member; @signature-params is the
// re-serialized Inner List. A directory response signature may cover
// "@authority";req, the authority of the request the response answers.
func wbaSignatureBase(method, rawURL string, h http.Header, member sfMember, directoryResponse bool) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", wbaFail(http.StatusForbidden, "", "request URL does not parse")
	}
	var b strings.Builder
	for _, c := range member.list {
		if c.value.kind != sfString {
			return "", wbaFail(http.StatusBadRequest, "", "component identifier must be an sf-string")
		}
		name := c.value.s
		var value string
		if strings.HasPrefix(name, "@") {
			reqParam := directoryResponse && name == componentAuthority && len(c.params) == 1 &&
				c.params[0].key == "req" && c.params[0].value.kind == sfBoolean && c.params[0].value.bo
			if len(c.params) != 0 && !reqParam {
				return "", wbaFail(http.StatusForbidden, "", "unsupported derived component parameters")
			}
			switch name {
			case componentMethod:
				value = strings.ToUpper(method)
			case componentTargetURI:
				// The target URI as received (RFC 9421 §2.2.2), not
				// canonicalized: a signer built on another HTTP stack signs
				// the URI it sent, percent-encoding included.
				value = rawURL
			case componentAuthority:
				// RFC 9421 §2.2.3: the authority normalized per HTTP
				// §4.2.3 (lowercase host, default port omitted).
				value = canonicalAuthority(u.Host, strings.ToLower(u.Scheme))
			default:
				return "", wbaFail(http.StatusForbidden, "", "unsupported derived component")
			}
		} else {
			lines := h.Values(name)
			if len(lines) == 0 || !isLowercaseFieldName(name) {
				return "", wbaFail(http.StatusForbidden, "", "missing covered field")
			}
			raw := strings.Join(lines, ", ")
			if strings.ContainsAny(raw, "\r\n\x00") {
				return "", wbaFail(http.StatusBadRequest, "", "covered field carries a control character")
			}
			switch {
			case len(c.params) == 0:
				value = strings.Trim(raw, " \t")
			case len(c.params) == 1 && c.params[0].key == "key" && c.params[0].value.kind == sfString:
				d, err := parseSFDictionary(raw)
				if err != nil {
					return "", wbaFail(http.StatusBadRequest, "", "covered dictionary field does not parse")
				}
				m, ok := d.get(c.params[0].value.s)
				if !ok {
					return "", wbaFail(http.StatusForbidden, "", "missing covered dictionary member")
				}
				value = m.serialize()
			default:
				return "", wbaFail(http.StatusForbidden, "", "unsupported field component parameters")
			}
		}
		b.WriteString(c.serialize())
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteByte('\n')
	}
	b.WriteString(`"@signature-params": `)
	b.WriteString(member.serialize())
	return b.String(), nil
}

// wbaKeyAlgorithm derives the signature algorithm from a directory key and
// checks the key may verify: Ed25519 (ed25519), the profile's one
// algorithm; use "sig" when present, no private member, a matching alg when
// present, and key_ops containing "verify" when present. Any other key is
// not a Web Bot Auth key: RSA is not part of the profile, and ES256 stays on
// the AdCP RFC 9421 profile.
func wbaKeyAlgorithm(jwk JWK) (Algorithm, error) {
	if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
		return "", wbaFail(http.StatusForbidden, "", "unsupported directory key")
	}
	alg, jwsAlg := AlgEd25519, string(jwkAlgEdDSA)
	if (jwk.Use != "" && jwk.Use != "sig") || jwk.PrivateD != "" || jwk.TestOnlyPrivateD != "" {
		return "", wbaFail(http.StatusForbidden, "", "directory key is not a public signing key")
	}
	// One key, one thumbprint: key members must be canonical unpadded
	// base64url, so the same key cannot appear under several thumbprints.
	for _, m := range []string{jwk.X, jwk.Y, jwk.N, jwk.E} {
		if m != "" && !canonicalB64URL(m) {
			return "", wbaFail(http.StatusForbidden, "", "directory key member is not canonical base64url")
		}
	}
	if jwk.Alg != "" && jwk.Alg != jwsAlg {
		return "", wbaFail(http.StatusForbidden, "", "directory key algorithm mismatch")
	}
	if jwk.KeyOps != nil && !containsString(jwk.KeyOps, "verify") {
		return "", wbaFail(http.StatusForbidden, "", "directory key cannot verify")
	}
	return alg, nil
}

func canonicalB64URL(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && base64.RawURLEncoding.EncodeToString(b) == s
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func wbaVerifyCrypto(member, signature sfMember, jwk JWK, base string) (Algorithm, error) {
	alg, err := wbaKeyAlgorithm(jwk)
	if err != nil {
		return "", err
	}
	if v, present := sfParamValue(member.params, "alg"); present && (v.kind != sfString || Algorithm(v.s) != alg) {
		return "", wbaFail(http.StatusForbidden, "", "signature algorithm does not match key")
	}
	if signature.inner || signature.item.value.kind != sfBinary {
		return "", wbaFail(http.StatusBadRequest, "", "signature must be sf-binary")
	}
	if !verifyWithJWK(alg, jwk, []byte(base), signature.item.value.b) {
		return "", wbaFail(http.StatusForbidden, "", "signature did not verify")
	}
	return alg, nil
}

// verifyWithJWK verifies sig over base under a Web Bot Auth directory key
// with the algorithm wbaKeyAlgorithm derived for it.
func verifyWithJWK(alg Algorithm, jwk JWK, base, sig []byte) bool {
	pub, err := jwk.PublicKey()
	return err == nil && verifySignature(alg, pub, base, sig)
}

// wbaDigestMatches reports whether the Content-Digest sha-256 member equals
// the SHA-256 of body.
func wbaDigestMatches(header string, body []byte) bool {
	if header == "" {
		return false
	}
	d, err := parseSFDictionary(header)
	if err != nil {
		return false
	}
	m, ok := d.get("sha-256")
	if !ok || m.inner || m.item.value.kind != sfBinary {
		return false
	}
	sum := sha256.Sum256(body)
	return bytes.Equal(m.item.value.b, sum[:])
}
