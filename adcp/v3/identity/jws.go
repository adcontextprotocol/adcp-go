package identity

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/psl"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/safefetch"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/strictjson"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
)

// JWSArtifact names an AdCP JWS artifact that specs/agent-identity-3-3.md R4
// moves to the issuer's Web Bot Auth key directory. It fixes which URL is
// the issuer's origin O and whether historical verification applies.
type JWSArtifact int

const (
	// GovernanceContext is a governance_context token (typ adcp-gov+jws).
	// O is the canonical origin of its iss claim, and historical
	// verification may use the governance key archive at O.
	GovernanceContext JWSArtifact = iota + 1
	// DesignatedResponse is designated-task response signing. O is the
	// canonical origin of agent_url.
	DesignatedResponse
	// RightsAttestation is a rights attestation. O is the canonical origin
	// of rights_agent.url: the holder's brand issues it, and its rights
	// agent signs it.
	RightsAttestation
)

// JWSProfile is the verification profile R4 selects for a JWS artifact.
type JWSProfile int

const (
	// JWSProfileWBA: the trust record derived from O lists O, so the JWS is
	// verified with a key from O's key directory, never under 3.2.
	JWSProfileWBA JWSProfile = iota + 1
	// JWSProfile32: no trust record lists O, so the JWS is verified under
	// the 3.2 profile (brand.json and jwks_uri), which stays fully
	// functional through 3.x.
	JWSProfile32
)

// ErrJWSProfile32 is returned by JWSVerifier.Verify when R4 selects the 3.2
// profile: the trust record derived from O was read and does not list O, the
// operator has never published one, or O has no registrable domain. The
// caller verifies the artifact with its 3.2 verifier. It is never returned
// while the record cannot be read, nor for an origin the verifier has bound
// (JWSVerifier.BoundOrigins).
var ErrJWSProfile32 = errors.New("identity: no trust record lists the issuer's origin; verify the JWS under the 3.2 profile")

// JWSError is a refusal of a JWS artifact on the Web Bot Auth path.
// Unavailable marks a failure to read the trust record, the key directory,
// or the archive: retryable, and never absence, so it never selects the 3.2
// profile. Revoked marks a key in O's revocation list.
type JWSError struct {
	Reason      string
	Unavailable bool
	Revoked     bool
	Wrapped     error
}

func (e *JWSError) Error() string { return "identity: JWS " + e.Reason }
func (e *JWSError) Unwrap() error { return e.Wrapped }

func jwsInvalid(reason string) *JWSError { return &JWSError{Reason: reason} }

// GovernanceKeyArchive looks up a key retired from O's key directory in the
// governance key archive at O (security.mdx §Revocation:
// {origin}/.well-known/jwks-archive.json). It returns (nil, nil) when the
// archive resolves and holds no key with that thumbprint, and an error when
// the archive cannot be read.
type GovernanceKeyArchive interface {
	ArchivedKey(ctx context.Context, origin, thumbprint string) (*signing.JWK, error)
}

// JWSVerifier verifies the three JWS artifacts of R4 for issuers whose
// origin a trust record lists. It reuses the request path's pieces: the
// trust record evaluator selects the profile, the key directory resolver
// supplies the key, and the origin revocation checker reports revocation.
//
// Selection (R4, R3a step 1) never reads an unsigned field: O comes from the
// URL the caller names for the artifact, and the trust record derived from
// O decides. A record that lists O selects this path. A record that was read
// and does not list O, an operator that has never published one, or an O
// with no registrable domain selects the 3.2 profile (ErrJWSProfile32),
// except for an origin the verifier has bound (BoundOrigins), which is
// refused instead (R4, no downgrade). A record that cannot be read now is a
// JWSError with Unavailable set, never the 3.2 profile, unless the last
// record read listed O and is within the evaluator's OutageContinuation
// bound: this path is then still selected, and VerifiedJWS.StaleTrustRecord
// is set so that a spend-committing surface can refuse, as R3a requires.
// Public deployments need a durable AuthorityHistory, so a 404 after a
// record was published is never read as an operator that never published
// one.
//
// Key rules: alg is EdDSA, kid is the key's JWK thumbprint, and the key is
// the one O's key directory publishes under that thumbprint. Purpose
// separation is by origin (D2), so use, key_ops, and adcp_use are not
// checked beyond the directory's own rules. Revocation is O's list: a
// revoked key is refused, and so is a key whose list state is not current
// unless the caller accepts that (JWSVerifyOptions.AcceptStaleRevocation).
//
// A verified JWS proves which origin signed it, not that the origin may act
// for anyone. Every other check of the surface is unchanged and stays with
// the caller: claims, aud, exp, replay, the relying-party record (the
// buyer's brand.json for a governance iss, the brand_domain brand.json for a
// designated response, the rights holder's for a rights attestation), and
// revoked_jtis, which OriginRevocationChecker does not retain (it keeps key
// revocations only).
type JWSVerifier struct {
	// Records evaluates the trust record derived from O. Required.
	Records *TrustRecordEvaluator
	// Keys resolves (O, thumbprint) in O's key directory. Required; usually
	// the signing.DirectoryResolver the request verifier uses.
	Keys signing.WBAKeyResolver
	// Revocation reports the key's state in O's revocation list. Required;
	// usually the signing.OriginRevocationChecker the request path uses.
	Revocation signing.WBARevocationChecker
	// BoundOrigins, when set, enforces R4's no downgrade: an origin bound to
	// a principal (R5) is never sent to the 3.2 profile. Usually the
	// signing.MiddlewareOptions.BoundOrigins store. A lookup error refuses.
	BoundOrigins signing.OriginBindingLookup
	// Archive is the governance key archive for historical verification of
	// a governance context. Nil disables historical verification.
	Archive GovernanceKeyArchive
}

// JWSVerifyOptions configures one JWSVerifier.Verify.
type JWSVerifyOptions struct {
	// Artifact is the kind of JWS. Required.
	Artifact JWSArtifact
	// SignerURL is the URL whose canonical origin is O: iss for a governance
	// context, agent_url for designated-task response signing, and
	// rights_agent.url for a rights attestation. Required. Take it from the
	// signed artifact or from the verifier's own context, never from an
	// unsigned field. For a governance context the verified payload's iss
	// must have the same canonical origin.
	SignerURL string
	// Typ is the exact protected-header typ the surface requires, for
	// example "adcp-gov+jws". Required; compared byte for byte.
	Typ string
	// Crit lists the protected-header extensions the caller processes. A
	// crit entry not listed here, or one the header does not carry, is
	// refused. A b64 header member is never accepted.
	Crit []string
	// Historical verifies a governance context after its key left O's key
	// directory: a key the directory does not publish is looked up in the
	// governance key archive at O. Only governance agents publish that
	// archive, so it applies to GovernanceContext only.
	Historical bool
	// AcceptStaleRevocation accepts a key whose revocation state is
	// RevocationStale or RevocationNoList, for a surface whose rules allow
	// it (for example, a governance intent-phase token with a short exp).
	// By default those states are refused as Unavailable. A revoked key,
	// or a state that cannot be read, is always refused.
	AcceptStaleRevocation bool
}

// VerifiedJWS is a JWS artifact verified on the Web Bot Auth path.
type VerifiedJWS struct {
	// Origin is O.
	Origin string
	// KeyID is the key's JWK thumbprint (the header kid).
	KeyID string
	// Header is the decoded protected header.
	Header map[string]json.RawMessage
	// Payload is the decoded payload. Its claims are not checked.
	Payload []byte
	// Revocation is the key's state in O's revocation list:
	// RevocationCurrent, or RevocationStale or RevocationNoList when the
	// caller set AcceptStaleRevocation.
	Revocation signing.RevocationState
	// Archived reports a key found in the governance key archive, not in
	// O's current key directory.
	Archived bool
	// StaleTrustRecord reports that the trust record derived from O could
	// not be read and the profile was selected from the last record that
	// listed O, within the evaluator's OutageContinuation bound. A
	// spend-committing surface refuses the artifact when it is set (R3a).
	StaleTrustRecord bool
}

// JWSSelection is the outcome of JWSVerifier.SelectProfile.
type JWSSelection struct {
	Profile JWSProfile
	// Origin is O.
	Origin string
	// StaleTrustRecord: see VerifiedJWS.StaleTrustRecord.
	StaleTrustRecord bool
}

const (
	// jwsMaxBytes caps a compact JWS. Governance tokens fit in 4096
	// characters; the other artifacts are of the same order.
	jwsMaxBytes = 64 << 10
	// jwsMaxProtectedHeader caps the encoded protected header.
	jwsMaxProtectedHeader = 8 << 10
	// jwsAlg is the one algorithm R4 allows: the directory holds Ed25519
	// keys only.
	jwsAlg = "EdDSA"
)

// SelectProfile applies R4's selection rule to signerURL: JWSProfileWBA when
// the trust record derived from O lists O, JWSProfile32 when no trust record
// lists O, and a JWSError when the record cannot be read now (a fetch
// failure is not absence) or O is bound and would be sent to 3.2.
func (v *JWSVerifier) SelectProfile(ctx context.Context, signerURL string) (JWSSelection, error) {
	if v.Records == nil {
		return JWSSelection{}, errJWSNotConfigured
	}
	origin, err := signing.CanonicalOrigin(signerURL)
	if err != nil || !strings.HasPrefix(origin, "https://") {
		return JWSSelection{}, jwsInvalid("signer URL has no canonical https origin")
	}
	sel := JWSSelection{Origin: origin}
	l := v.Records.checkListing(ctx, origin, func(r *TrustRecord) bool { return r.listsOrigin(origin) }, "")
	switch {
	case l.Status == Listed:
		sel.Profile = JWSProfileWBA
		return sel, nil
	case l.Status == Unavailable && l.WithinGrace:
		sel.Profile, sel.StaleTrustRecord = JWSProfileWBA, true
		return sel, nil
	case l.Status == Unlisted && !IsTrustRecordUnavailable(l.Err):
		// The record was read (or was never published, or O has no
		// registrable domain). An Unlisted outcome that carries a fetch
		// failure comes from the last record read; R4 never selects 3.2
		// while the record cannot be read.
		if v.BoundOrigins != nil {
			_, bound, berr := v.BoundOrigins.PrincipalForOrigin(ctx, origin)
			if berr != nil {
				return sel, &JWSError{Reason: "origin binding lookup failed", Unavailable: true, Wrapped: berr}
			}
			if bound {
				return sel, jwsInvalid("origin is bound and is never verified under the 3.2 profile")
			}
		}
		sel.Profile = JWSProfile32
		return sel, nil
	default:
		return sel, &JWSError{Reason: "trust record unavailable", Unavailable: true, Wrapped: l.Err}
	}
}

var errJWSNotConfigured = errors.New("identity: JWSVerifier requires Records, Keys, and Revocation")

// Verify verifies a compact JWS artifact under R4. It returns
// ErrJWSProfile32 when the 3.2 profile applies, and a *JWSError for a
// refusal or an outage.
func (v *JWSVerifier) Verify(ctx context.Context, jws string, opts JWSVerifyOptions) (*VerifiedJWS, error) {
	if v.Records == nil || v.Keys == nil || v.Revocation == nil {
		return nil, errJWSNotConfigured
	}
	switch opts.Artifact {
	case GovernanceContext, DesignatedResponse, RightsAttestation:
	default:
		return nil, errors.New("identity: unknown JWS artifact")
	}
	if opts.Typ == "" {
		return nil, errors.New("identity: JWSVerifyOptions.Typ is required")
	}
	if opts.Historical && opts.Artifact != GovernanceContext {
		return nil, errors.New("identity: historical verification applies to a governance context only")
	}
	if opts.Historical && v.Archive == nil {
		return nil, errors.New("identity: historical verification requires an Archive")
	}

	// The token is parsed before anything is fetched, so a malformed token
	// costs no network access.
	hdrB64, payloadB64, sigB64, header, err := parseCompactJWS(jws, opts)
	if err != nil {
		return nil, err
	}
	var kid string
	if json.Unmarshal(header["kid"], &kid) != nil || !isThumbprint(kid) {
		return nil, jwsInvalid("kid is not a JWK thumbprint")
	}
	payload, _ := base64.RawURLEncoding.Strict().DecodeString(payloadB64)
	if err := strictjson.Validate(payload, 0); err != nil {
		return nil, jwsInvalid("payload is not strict JSON")
	}

	sel, err := v.SelectProfile(ctx, opts.SignerURL)
	if err != nil {
		return nil, err
	}
	if sel.Profile == JWSProfile32 {
		return nil, ErrJWSProfile32
	}
	origin := sel.Origin

	jwk, err := v.Keys.ResolveWBAKey(ctx, origin, kid)
	if err != nil {
		return nil, &JWSError{Reason: "key directory unavailable", Unavailable: true, Wrapped: err}
	}
	archived := false
	if jwk == nil && opts.Historical {
		if jwk, err = v.Archive.ArchivedKey(ctx, origin, kid); err != nil {
			return nil, &JWSError{Reason: "governance key archive unavailable", Unavailable: true, Wrapped: err}
		}
		archived = jwk != nil
	}
	if jwk == nil {
		return nil, jwsInvalid("key is not in the issuer's key directory")
	}
	pub, err := ed25519Key(*jwk)
	if err != nil {
		return nil, jwsInvalid("key is not an Ed25519 key")
	}
	if tp, err := signing.JWKThumbprint(*jwk); err != nil || tp != kid {
		return nil, jwsInvalid("kid is not the key's JWK thumbprint")
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(sigB64)
	if err != nil || !ed25519.Verify(pub, []byte(hdrB64+"."+payloadB64), sig) {
		return nil, jwsInvalid("signature did not verify")
	}

	if opts.Artifact == GovernanceContext {
		// Read iss by its exact name: encoding/json would match "ISS".
		var claims map[string]json.RawMessage
		var iss string
		if err := json.Unmarshal(payload, &claims); err != nil || json.Unmarshal(claims["iss"], &iss) != nil {
			return nil, jwsInvalid("payload has no iss")
		}
		if o, err := signing.CanonicalOrigin(iss); err != nil || o != origin {
			return nil, jwsInvalid("iss is not at the issuer's origin")
		}
	}

	state, rerr := v.Revocation.CheckWBAKey(ctx, origin, kid)
	switch {
	case state == signing.RevocationRevoked:
		return nil, &JWSError{Reason: "signing key revoked", Revoked: true, Wrapped: rerr}
	case state == signing.RevocationCurrent:
	case (state == signing.RevocationStale || state == signing.RevocationNoList) && opts.AcceptStaleRevocation:
	default:
		return nil, &JWSError{Reason: "revocation list " + state.String(), Unavailable: true, Wrapped: rerr}
	}
	return &VerifiedJWS{Origin: origin, KeyID: kid, Header: header, Payload: payload, Revocation: state,
		Archived: archived, StaleTrustRecord: sel.StaleTrustRecord}, nil
}

// parseCompactJWS splits a compact JWS and checks its protected header: every
// segment non-empty canonical unpadded base64url, a strict-JSON header of at
// most 8 KiB with alg EdDSA, the exact typ, no b64 member, and only
// understood crit extensions.
func parseCompactJWS(jws string, opts JWSVerifyOptions) (hdrB64, payloadB64, sigB64 string, header map[string]json.RawMessage, err error) {
	if len(jws) > jwsMaxBytes {
		return "", "", "", nil, jwsInvalid("is too large")
	}
	parts := strings.Split(jws, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", nil, jwsInvalid("is not a compact JWS")
	}
	for _, p := range parts {
		if !canonicalB64URL(p) {
			return "", "", "", nil, jwsInvalid("segment is not canonical base64url")
		}
	}
	if len(parts[0]) > jwsMaxProtectedHeader {
		return "", "", "", nil, jwsInvalid("protected header is too large")
	}
	raw, _ := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err := strictjson.Unmarshal(raw, 0, &header); err != nil || header == nil {
		return "", "", "", nil, jwsInvalid("protected header is not a strict JSON object")
	}
	var alg, typ string
	if json.Unmarshal(header["alg"], &alg) != nil || alg != jwsAlg {
		return "", "", "", nil, jwsInvalid("alg is not EdDSA")
	}
	if json.Unmarshal(header["typ"], &typ) != nil || typ != opts.Typ {
		return "", "", "", nil, jwsInvalid("typ mismatch")
	}
	if _, ok := header["b64"]; ok {
		return "", "", "", nil, jwsInvalid("b64 is not supported")
	}
	if c, ok := header["crit"]; ok {
		var crit []string
		if json.Unmarshal(c, &crit) != nil || len(crit) == 0 {
			return "", "", "", nil, jwsInvalid("crit is not a non-empty string array")
		}
		seen := map[string]bool{}
		for _, name := range crit {
			if seen[name] || name == "b64" || !containsStr(opts.Crit, name) {
				return "", "", "", nil, jwsInvalid(fmt.Sprintf("crit extension %q is not understood", name))
			}
			if _, ok := header[name]; !ok {
				return "", "", "", nil, jwsInvalid(fmt.Sprintf("crit extension %q is missing from the header", name))
			}
			seen[name] = true
		}
	}
	return parts[0], parts[1], parts[2], header, nil
}

func canonicalB64URL(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && base64.RawURLEncoding.EncodeToString(b) == s
}

// isThumbprint reports whether s has the shape of a SHA-256 JWK thumbprint:
// 43 characters of canonical unpadded base64url.
func isThumbprint(s string) bool {
	return len(s) == 43 && canonicalB64URL(s)
}

// ed25519Key returns jwk's Ed25519 public key. The key directory admits only
// Ed25519 keys; the archive is checked here.
func ed25519Key(jwk signing.JWK) (ed25519.PublicKey, error) {
	if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" || jwk.PrivateD != "" || jwk.TestOnlyPrivateD != "" {
		return nil, errors.New("not an Ed25519 public key")
	}
	if jwk.Alg != "" && jwk.Alg != jwsAlg {
		return nil, errors.New("key alg is not EdDSA")
	}
	x, err := base64.RawURLEncoding.Strict().DecodeString(jwk.X)
	if err != nil || len(x) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(x) != jwk.X {
		return nil, errors.New("invalid Ed25519 x")
	}
	return ed25519.PublicKey(x), nil
}

// listsOrigin reports whether the record lists origin in agents[], with any
// role.
func (r *TrustRecord) listsOrigin(origin string) bool {
	for _, a := range r.Agents {
		if o, err := signing.CanonicalOrigin(a.URL); err == nil && o == origin {
			return true
		}
	}
	return false
}

// GovernanceKeyArchivePath is the well-known path of a governance agent's
// key archive (security.mdx §Revocation).
const GovernanceKeyArchivePath = "/.well-known/jwks-archive.json"

const archiveMaxBytes = 256 << 10

// archivePrivateMembers are the private JWK members, folded as encoding/json
// matches struct fields, that fail an archive as they fail a key directory.
var archivePrivateMembers = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, n := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k", "_private_d_for_test_only"} {
		m[strictjson.Fold(n)] = struct{}{}
	}
	return m
}()

// ArchiveFetcher fetches a governance key archive URL. It is a trusted
// transport seam: a custom fetcher must enforce the fetch rules (HTTPS, SSRF
// checks, no redirects, a 256 KiB cap). The default does.
type ArchiveFetcher func(ctx context.Context, url string) (*TrustResponse, error)

// HTTPGovernanceKeyArchive reads the governance key archive at
// O + GovernanceKeyArchivePath on every lookup. Historical verification is
// an audit path, so it does not cache. At most MaxConcurrent fetches run at
// once (default 4); a lookup waits for a slot until its context ends. One
// operator (a registrable domain) has at most one fetch in flight, and a
// second lookup for it fails as unavailable, so one operator serving slow
// archives cannot hold every slot.
type HTTPGovernanceKeyArchive struct {
	// Fetch defaults to an SSRF-safe HTTPS fetch with a 256 KiB cap.
	Fetch ArchiveFetcher
	// MaxConcurrent bounds concurrent archive fetches. Defaults to 4.
	MaxConcurrent int

	once     sync.Once
	fetch    ArchiveFetcher
	slots    chan struct{}
	mu       sync.Mutex
	inFlight map[string]bool
}

func (a *HTTPGovernanceKeyArchive) init() {
	a.fetch = a.Fetch
	if a.fetch == nil {
		client := safefetch.NewClient()
		a.fetch = func(ctx context.Context, url string) (*TrustResponse, error) {
			r, err := safefetch.Get(ctx, client, url, safefetch.Options{Accept: "application/jwk-set+json, application/json", MaxBytes: archiveMaxBytes})
			if err != nil {
				return nil, err
			}
			return &TrustResponse{Status: r.Status, Body: r.Body}, nil
		}
	}
	n := a.MaxConcurrent
	if n <= 0 {
		n = 4
	}
	a.slots = make(chan struct{}, n)
	a.inFlight = map[string]bool{}
}

// ArchivedKey implements GovernanceKeyArchive. The archive is a JWK Set.
// Keys are matched by their computed JWK thumbprint, and an entry must carry
// that thumbprint as its kid: any other entry is skipped, because its
// revocation would be published under another kid (or none), which the
// origin's revocation list does not retain for Web Bot Auth keys. An entry carrying
// private key material fails the whole archive, as it does a key directory.
// A 404 or 410 means the origin publishes no archive (it is a SHOULD): the
// key is not found.
func (a *HTTPGovernanceKeyArchive) ArchivedKey(ctx context.Context, origin, thumbprint string) (*signing.JWK, error) {
	a.once.Do(a.init)
	operator, ok := psl.RegistrableDomain(origin)
	if !ok {
		operator = origin
	}
	a.mu.Lock()
	if a.inFlight[operator] {
		a.mu.Unlock()
		return nil, errors.New("identity: governance key archive fetch capacity exceeded for operator")
	}
	a.inFlight[operator] = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.inFlight, operator)
		a.mu.Unlock()
	}()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	res, err := a.fetch(ctx, origin+GovernanceKeyArchivePath)
	if err != nil {
		return nil, err
	}
	if res.Status == http.StatusNotFound || res.Status == http.StatusGone {
		return nil, nil
	}
	if res.Status != http.StatusOK || len(res.Body) > archiveMaxBytes {
		return nil, fmt.Errorf("identity: governance key archive unavailable (HTTP %d)", res.Status)
	}
	var doc struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := strictjson.Unmarshal(res.Body, 0, &doc); err != nil {
		return nil, errors.New("identity: governance key archive is not strict JSON")
	}
	var found *signing.JWK
	for _, raw := range doc.Keys {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil {
			return nil, errors.New("identity: governance key archive entry is not an object")
		}
		for name := range members {
			if _, private := archivePrivateMembers[strictjson.Fold(name)]; private {
				return nil, errors.New("identity: governance key archive entry carries private key material")
			}
		}
		var jwk signing.JWK
		if json.Unmarshal(raw, &jwk) != nil {
			continue
		}
		if _, err := ed25519Key(jwk); err != nil {
			continue
		}
		tp, err := signing.JWKThumbprint(jwk)
		if err != nil || tp != thumbprint || jwk.Kid != tp || found != nil {
			continue
		}
		jwk.Kid = tp
		found = &jwk
	}
	return found, nil
}
