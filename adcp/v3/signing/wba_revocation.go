package signing

import (
	"bytes"
	"container/list"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/psl"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/safefetch"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/internal/strictjson"
)

const (
	// RevocationListPath is the well-known path of an identity's signed
	// revocation list.
	RevocationListPath = "/.well-known/governance-revocations.json"

	// RevocationListTyp is the JWS typ of a revocation list.
	RevocationListTyp = "adcp-gov-revocation+jws"

	revocationMaxBytes        = 1 << 20
	revocationMinInterval     = 60 * time.Second
	revocationMaxInterval     = 15 * time.Minute
	defaultRevocationGrace    = 2.0
	revocationClockSkew       = 60 * time.Second
	defaultRevocationOrigins  = 256
	defaultOriginsPerOperator = 8
)

// RFC 5280 §5.3.1 revocation reason codes, in the JSON spelling of RFC 8555
// §7.6.
const (
	RevocationReasonUnspecified          = "unspecified"
	RevocationReasonKeyCompromise        = "keyCompromise"
	RevocationReasonCACompromise         = "cACompromise"
	RevocationReasonAffiliationChanged   = "affiliationChanged"
	RevocationReasonSuperseded           = "superseded"
	RevocationReasonCessationOfOperation = "cessationOfOperation"
	RevocationReasonCertificateHold      = "certificateHold"
	RevocationReasonRemoveFromCRL        = "removeFromCRL"
	RevocationReasonPrivilegeWithdrawn   = "privilegeWithdrawn"
	RevocationReasonAACompromise         = "aACompromise"
)

var revocationReasons = map[string]struct{}{
	RevocationReasonUnspecified: {}, RevocationReasonKeyCompromise: {}, RevocationReasonCACompromise: {},
	RevocationReasonAffiliationChanged: {}, RevocationReasonSuperseded: {}, RevocationReasonCessationOfOperation: {},
	RevocationReasonCertificateHold: {}, RevocationReasonRemoveFromCRL: {}, RevocationReasonPrivilegeWithdrawn: {},
	RevocationReasonAACompromise: {},
}

// RevocationDetails is an optional revocation_details entry: an RFC 5280
// reason and the date from which the key is known or suspected to be
// compromised (RFC 5280 §5.3.2). EXPERIMENTAL (AdCP 3.3).
type RevocationDetails struct {
	Reason         string     `json:"reason,omitempty"`
	InvalidityDate *time.Time `json:"invalidity_date,omitempty"`
}

// Merge returns the most severe combination of d and newer: keyCompromise
// if either says so, otherwise the earlier-seen reason, and the earliest
// invalidity date. A detail is never withdrawn or weakened.
func (d RevocationDetails) Merge(newer RevocationDetails) RevocationDetails {
	out := RevocationDetails{Reason: d.Reason}
	if d.Reason == RevocationReasonKeyCompromise || newer.Reason == RevocationReasonKeyCompromise {
		out.Reason = RevocationReasonKeyCompromise
	} else if out.Reason == "" {
		out.Reason = newer.Reason
	}
	switch {
	case d.InvalidityDate == nil:
		out.InvalidityDate = newer.InvalidityDate
	case newer.InvalidityDate == nil || d.InvalidityDate.Before(*newer.InvalidityDate):
		out.InvalidityDate = d.InvalidityDate
	default:
		out.InvalidityDate = newer.InvalidityDate
	}
	return out
}

// Exposes reports whether a request the seller received at receivedAt,
// verified under a key with these details, is suspect: the key was revoked
// for keyCompromise and the request arrived at or after the invalidity
// date, or at any time when there is none. receivedAt is the seller's
// receipt time, never the signature's created, which the signer controls.
// Any other reason exposes nothing.
func (d RevocationDetails) Exposes(receivedAt time.Time) bool {
	return d.Reason == RevocationReasonKeyCompromise &&
		(d.InvalidityDate == nil || !receivedAt.Before(*d.InvalidityDate))
}

// RevocationList is a verified revocation list payload. EXPERIMENTAL in its
// revocation_details handling (AdCP 3.3).
type RevocationList struct {
	Version     int
	Issuer      string
	Updated     time.Time
	NextUpdate  time.Time
	RevokedKids map[string]struct{}
	RevokedJTIs map[string]struct{}
	Details     map[string]RevocationDetails
}

// IsRevoked reports whether keyID is revoked.
func (l *RevocationList) IsRevoked(keyID string) bool {
	_, ok := l.RevokedKids[keyID]
	return ok
}

// Retain folds previous into l so a revocation is never withdrawn and a
// detail never weakened: every key and jti previous revoked stays revoked,
// and each key keeps the most severe detail either list carries.
func (l *RevocationList) Retain(previous *RevocationList) error {
	if previous == nil {
		return nil
	}
	if l.Issuer != previous.Issuer {
		return errors.New("signing: cannot merge revocation lists from different issuers")
	}
	for k := range previous.RevokedKids {
		l.RevokedKids[k] = struct{}{}
	}
	for j := range previous.RevokedJTIs {
		l.RevokedJTIs[j] = struct{}{}
	}
	for kid, old := range previous.Details {
		if cur, ok := l.Details[kid]; ok {
			l.Details[kid] = old.Merge(cur)
		} else {
			l.Details[kid] = old
		}
	}
	return nil
}

// ParseRevocationList validates a revocation list payload (the JSON inside
// the JWS) for issuer: a positive version, an issuer whose canonical origin
// is issuer, RFC 3339 updated and next_update at least 60 s apart, string
// arrays of revoked kids and jtis, and revocation_details keyed only by
// revoked kids, with known RFC 5280 reasons and timezone-qualified dates.
func ParseRevocationList(payload []byte, issuer string) (*RevocationList, error) {
	var raw struct {
		Version     *int                       `json:"version"`
		Issuer      *string                    `json:"issuer"`
		Updated     *string                    `json:"updated"`
		NextUpdate  *string                    `json:"next_update"`
		RevokedKids []string                   `json:"revoked_kids"`
		RevokedJTIs []string                   `json:"revoked_jtis"`
		Details     map[string]json.RawMessage `json:"revocation_details"`
	}
	if err := strictjson.Unmarshal(payload, 0, &raw); err != nil {
		return nil, fmt.Errorf("signing: revocation list payload: %w", err)
	}
	if raw.Version == nil || *raw.Version < 1 {
		return nil, errors.New("signing: revocation list version must be a positive integer")
	}
	if raw.Issuer == nil || raw.Updated == nil || raw.NextUpdate == nil {
		return nil, errors.New("signing: revocation list requires issuer, updated, and next_update")
	}
	if u, err := url.Parse(*raw.Issuer); err != nil || u.User != nil {
		return nil, errors.New("signing: revocation list issuer is not a valid origin")
	}
	got, err := CanonicalOrigin(*raw.Issuer)
	if err != nil || got != issuer {
		return nil, errors.New("signing: revocation list issuer does not match the identity")
	}
	updated, err1 := time.Parse(time.RFC3339Nano, *raw.Updated)
	next, err2 := time.Parse(time.RFC3339Nano, *raw.NextUpdate)
	if err1 != nil || err2 != nil {
		return nil, errors.New("signing: revocation list timestamps must be RFC 3339")
	}
	if next.Sub(updated) < revocationMinInterval {
		return nil, errors.New("signing: revocation list declared cadence is below the 60 s floor")
	}
	l := &RevocationList{
		Version:     *raw.Version,
		Issuer:      issuer,
		Updated:     updated,
		NextUpdate:  next,
		RevokedKids: map[string]struct{}{},
		RevokedJTIs: map[string]struct{}{},
		Details:     map[string]RevocationDetails{},
	}
	for _, k := range raw.RevokedKids {
		l.RevokedKids[k] = struct{}{}
	}
	for _, j := range raw.RevokedJTIs {
		l.RevokedJTIs[j] = struct{}{}
	}
	for kid, rawDetail := range raw.Details {
		if _, ok := l.RevokedKids[kid]; !ok {
			return nil, errors.New("signing: revocation_details must describe revoked_kids only")
		}
		var d struct {
			Reason         *string `json:"reason"`
			InvalidityDate *string `json:"invalidity_date"`
		}
		if err := json.Unmarshal(rawDetail, &d); err != nil || bytes.TrimSpace(rawDetail)[0] != '{' {
			return nil, errors.New("signing: revocation_details entry must be an object")
		}
		var detail RevocationDetails
		if d.Reason != nil {
			if _, ok := revocationReasons[*d.Reason]; !ok {
				return nil, errors.New("signing: unknown RFC 5280 revocation reason")
			}
			detail.Reason = *d.Reason
		}
		if d.InvalidityDate != nil {
			t, err := time.Parse(time.RFC3339Nano, *d.InvalidityDate)
			if err != nil {
				return nil, errors.New("signing: invalidity_date must be an RFC 3339 timestamp with a timezone")
			}
			detail.InvalidityDate = &t
		}
		l.Details[kid] = detail
	}
	return l, nil
}

// revocationJWSAlgorithms are the JWS algorithms a revocation list may be
// signed with: EdDSA, the algorithm of the one key type a Web Bot Auth key
// directory holds (Ed25519).
var revocationJWSAlgorithms = []jose.SignatureAlgorithm{jose.EdDSA}

// revocationMaxProtectedHeader caps the encoded protected header of a
// revocation list JWS. A real header (alg, typ, kid) is under 200 bytes.
// go-jose decodes every header member into Go values, twice, before the
// signature is checked, so an unbounded header lets anyone who serves a
// revocation list make a verifier allocate many times the 1 MiB body.
const revocationMaxProtectedHeader = 8 << 10

// verifyRevocationJWS verifies a revocation list JWS in compact, flattened
// JSON, or single-signature general JSON serialization and returns its
// payload. The protected header must carry alg EdDSA, typ
// adcp-gov-revocation+jws, a kid, and no crit; the kid is resolved by
// resolve and the key's algorithm must match alg.
//
// go-jose parses the protected header, applies the algorithm allowlist and
// builds the RFC 7515 signing input. AdCP's envelope rules run first, and
// the JWS reaches go-jose in compact form:
//   - a JSON serialization must be strict JSON (strictjson), as every
//     document carrying authority is, with exactly one signature, and may
//     not mix the flattened members (protected, signature) with the
//     general one (signatures), which RFC 7515 §7.2.2 forbids; go-jose
//     would ignore the flattened members. An unprotected header is
//     ignored, as before: nothing is read from it;
//   - the encoded protected header is at most 8 KiB
//     (revocationMaxProtectedHeader), checked before anything decodes it;
//   - every segment must be canonical unpadded base64url, and a compact
//     segment non-empty. go-jose decodes leniently (it strips whitespace,
//     and ignores unused low bits) and rebuilds the signing input from
//     the decoded bytes, so a non-canonical segment would verify as a
//     different string than the one sent;
//   - the decoded protected header must be strict JSON (case-folded
//     duplicate names, nesting); go-jose rejects only exact duplicates;
//   - alg, typ and kid come from the protected header;
//   - a non-empty crit is refused, as the profile defines no extension;
//   - "b64": false is refused: go-jose would sign over the raw payload
//     (RFC 7797) even when crit does not list b64, where the profile always
//     signs over the base64url payload.
//
// go-jose also refuses a protected header whose jwk, x5c or nonce member
// does not parse; the profile reads none of them.
//
// The signature itself is checked by verifyWithJWK, the same key policy
// and verification Web Bot Auth HTTP signatures use.
func verifyRevocationJWS(body []byte, resolve func(kid string) (*JWK, error)) ([]byte, error) {
	body = bytes.TrimSpace(body)
	var protectedB64, payloadB64, signatureB64 string
	if len(body) > 0 && body[0] == '{' {
		var doc struct {
			Payload    *string `json:"payload"`
			Protected  *string `json:"protected"`
			Signature  *string `json:"signature"`
			Signatures []struct {
				Protected *string `json:"protected"`
				Signature *string `json:"signature"`
			} `json:"signatures"`
		}
		if err := strictjson.Unmarshal(body, 0, &doc); err != nil || doc.Payload == nil {
			return nil, errors.New("signing: revocation list is not a JWS")
		}
		payloadB64 = *doc.Payload
		switch {
		case doc.Signatures != nil && doc.Protected == nil && doc.Signature == nil:
			if len(doc.Signatures) != 1 || doc.Signatures[0].Protected == nil || doc.Signatures[0].Signature == nil {
				return nil, errors.New("signing: revocation list JWS must carry exactly one signature")
			}
			protectedB64, signatureB64 = *doc.Signatures[0].Protected, *doc.Signatures[0].Signature
		case doc.Signatures == nil && doc.Protected != nil && doc.Signature != nil:
			protectedB64, signatureB64 = *doc.Protected, *doc.Signature
		default:
			return nil, errors.New("signing: revocation list JWS serialization is ambiguous")
		}
	} else {
		parts := strings.Split(string(body), ".")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, errors.New("signing: revocation list compact JWS is malformed")
		}
		protectedB64, payloadB64, signatureB64 = parts[0], parts[1], parts[2]
	}
	if len(protectedB64) > revocationMaxProtectedHeader {
		return nil, errors.New("signing: JWS protected header is too large")
	}
	for _, segment := range []string{protectedB64, payloadB64, signatureB64} {
		if !canonicalB64URL(segment) {
			return nil, errors.New("signing: revocation list JWS segment is not canonical base64url")
		}
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(protectedB64)
	if err != nil || strictjson.Validate(headerJSON, 0) != nil {
		return nil, errors.New("signing: JWS protected header is not strict JSON")
	}
	jws, err := jose.ParseSignedCompact(protectedB64+"."+payloadB64+"."+signatureB64, revocationJWSAlgorithms)
	if err != nil {
		return nil, errors.New("signing: revocation list is not a JWS")
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("signing: revocation list JWS must carry exactly one signature")
	}
	protected := jws.Signatures[0].Protected
	if jose.SignatureAlgorithm(protected.Algorithm) != jose.EdDSA {
		return nil, errors.New("signing: JWS alg not allowed")
	}
	want := AlgEd25519
	if typ, _ := protected.ExtraHeaders[jose.HeaderType].(string); typ != RevocationListTyp {
		return nil, errors.New("signing: JWS typ is not " + RevocationListTyp)
	}
	if crit, ok := protected.ExtraHeaders["crit"]; ok {
		if list, isList := crit.([]any); !isList || len(list) > 0 {
			return nil, errors.New("signing: JWS crit is not supported")
		}
	}
	if b64, ok := protected.ExtraHeaders["b64"]; ok && b64 == false {
		return nil, errors.New("signing: JWS b64 is not supported")
	}
	if protected.KeyID == "" {
		return nil, errors.New("signing: JWS protected header must carry a kid")
	}
	jwk, err := resolve(protected.KeyID)
	if err != nil {
		return nil, err
	}
	if jwk == nil {
		return nil, errors.New("signing: JWS kid is not in the identity's key directory")
	}
	keyAlg, err := wbaKeyAlgorithm(*jwk)
	if err != nil || keyAlg != want {
		return nil, errors.New("signing: JWS key does not match alg")
	}
	payload, err := jws.Verify(directoryKeyVerifier{alg: want, jwk: *jwk})
	if err != nil {
		return nil, errors.New("signing: revocation list signature did not verify")
	}
	return payload, nil
}

// directoryKeyVerifier is a jose.OpaqueVerifier that checks a JWS signature
// with verifyWithJWK.
type directoryKeyVerifier struct {
	alg Algorithm
	jwk JWK
}

func (v directoryKeyVerifier) VerifyPayload(signingInput, signature []byte, _ jose.SignatureAlgorithm) error {
	if !verifyWithJWK(v.alg, v.jwk, signingInput, signature) {
		return errors.New("signing: signature did not verify")
	}
	return nil
}

// RevocationFetchResult is one revocation list fetch.
type RevocationFetchResult struct {
	Status       int
	Body         []byte
	ETag         string
	LastModified string
}

// RevocationFetcher fetches a revocation list URL, sending If-None-Match
// and If-Modified-Since when non-empty. It is a trusted transport seam; the
// default enforces HTTPS, SSRF checks, no redirects, and a 1 MiB cap.
type RevocationFetcher func(ctx context.Context, url, ifNoneMatch, ifModifiedSince string) (*RevocationFetchResult, error)

// RevocationHistory persists the revocations an OriginRevocationChecker has
// seen, so "never un-revoke" survives eviction and restarts. Store is called
// after every accepted list with the retained (merged) list; Load is called
// when an origin is admitted. The default keeps a bounded in-memory history.
type RevocationHistory interface {
	LoadRevocations(ctx context.Context, origin string) (*RevocationList, error)
	StoreRevocations(ctx context.Context, origin string, list *RevocationList) error
}

// OriginRevocationCheckerOptions configures NewOriginRevocationChecker.
type OriginRevocationCheckerOptions struct {
	// Keys resolves the list's JWS kid in the identity's own key directory.
	// Required; usually the same DirectoryResolver the verifier uses.
	Keys WBAKeyResolver
	// Fetch defaults to an SSRF-safe HTTPS fetch.
	Fetch RevocationFetcher
	// MaxOrigins bounds origins tracked at once (default 256). At the
	// bound the least recently used origin is evicted, its revocations
	// kept in History, so admission never locks out new origins.
	MaxOrigins int
	// MaxOriginsPerOperator bounds origins of one operator (default 8):
	// one registrable domain, with every tenant of a PSL PRIVATE hosting
	// suffix counted together. Beyond it that operator's least recently used origin is
	// evicted, so one operator cannot crowd out others. It also bounds
	// concurrent first discoveries per operator.
	MaxOriginsPerOperator int
	// GraceMultiplier sets the point at which a list becomes
	// RevocationStale: next_update plus this many polling intervals. Defaults to 2, as in the Python SDK.
	GraceMultiplier float64
	// History persists retained revocations. Defaults to an in-memory
	// history of 4 x MaxOrigins origins with per-operator quotas that
	// refuse rather than evict (an origin that does not fit is
	// RevocationStale until a store succeeds, while its verified list keeps
	// revoking). A History that cannot be read makes the state
	// RevocationUnavailable.
	// It does not survive restarts or span replicas, so public deployments
	// must supply a durable one.
	History RevocationHistory
	// Clock defaults to time.Now.
	Clock func() time.Time
}

// OriginRevocationChecker reads each Web Bot Auth identity's signed
// revocation list at its own origin (RevocationListPath, typ
// adcp-gov-revocation+jws, signed by a key in that origin's directory) and
// implements WBARevocationChecker.
//
// Revocation is monotonic: every revoked key and detail seen for an origin
// is retained across refreshes and across evictions through History (the
// default in-memory History refuses an origin that would exceed its quotas,
// which is then RevocationStale, rather than forgetting another's), a list older
// than the retained one is refused, and keyCompromise and the
// earliest invalidity_date win. Only thumbprint-shaped kids (the only kids
// a Web Bot Auth keyid can match) are retained, at most 1024 per origin; a
// list over the bound fails that origin closed. Refreshes are single-flight
// per origin, detached from the caller, and never hold a lock across I/O.
// Past next_update plus the grace, the state is RevocationStale, which
// fails spend-committing calls closed in the authorization layer.
type OriginRevocationChecker struct {
	keys           WBAKeyResolver
	fetch          RevocationFetcher
	maxOrigins     int
	maxPerOperator int
	grace          float64
	history        RevocationHistory
	clock          func() time.Time

	mu       sync.Mutex
	origins  map[string]*originRevocations
	lru      *list.List // front = most recently used
	failures map[string]revocationFailure
	pending  map[string]*revocationCall
}

// revocationFailure is a discovery cooldown. noList records that the origin
// serves no revocation list (404 or 410 with no retained history), as
// opposed to a list that could not be read.
type revocationFailure struct {
	until  time.Time
	noList bool
}

// errNoRevocationList is a revocation list fetch answered 404 or 410.
var errNoRevocationList = errors.New("signing: origin serves no revocation list")

// errRevocationStore is a RevocationHistory read failure.
var errRevocationStore = errors.New("signing: revocation history unavailable")

type revocationCall struct {
	done chan struct{}
	err  error
}

type originRevocations struct {
	origin string
	elem   *list.Element // guarded by OriginRevocationChecker.mu

	mu           sync.Mutex // guards the fields below; never held across I/O
	current      *RevocationList
	etag         string
	lastModified string
	lastAttempt  time.Time
	interval     time.Duration
	refreshing   *revocationCall
	// unpersisted is set while History refuses or fails to store the
	// retained list: the origin is then RevocationStale, since its revocations
	// would not survive an eviction or restart.
	unpersisted error
}

const (
	maxRetainedRevocations = 1024
	defaultHistoryFactor   = 4
	historyKidBudget       = 1 << 18
)

// NewOriginRevocationChecker returns an OriginRevocationChecker.
func NewOriginRevocationChecker(opts OriginRevocationCheckerOptions) (*OriginRevocationChecker, error) {
	if opts.Keys == nil {
		return nil, errors.New("signing: OriginRevocationChecker requires Keys")
	}
	c := &OriginRevocationChecker{
		keys:           opts.Keys,
		fetch:          opts.Fetch,
		maxOrigins:     opts.MaxOrigins,
		maxPerOperator: opts.MaxOriginsPerOperator,
		grace:          opts.GraceMultiplier,
		history:        opts.History,
		clock:          opts.Clock,
		origins:        map[string]*originRevocations{},
		lru:            list.New(),
		failures:       map[string]revocationFailure{},
		pending:        map[string]*revocationCall{},
	}
	if c.maxOrigins <= 0 {
		c.maxOrigins = defaultRevocationOrigins
	}
	if c.maxPerOperator <= 0 {
		c.maxPerOperator = defaultOriginsPerOperator
	}
	if c.grace <= 0 {
		c.grace = defaultRevocationGrace
	}
	if c.clock == nil {
		c.clock = time.Now
	}
	if c.history == nil {
		c.history = newMemoryRevocationHistory(defaultHistoryFactor * c.maxOrigins)
	}
	if c.fetch == nil {
		client := safefetch.NewClient()
		c.fetch = func(ctx context.Context, u, inm, ims string) (*RevocationFetchResult, error) {
			h := http.Header{}
			if inm != "" {
				h.Set("If-None-Match", inm)
			}
			if ims != "" {
				h.Set("If-Modified-Since", ims)
			}
			r, err := safefetch.Get(ctx, client, u, safefetch.Options{
				Accept:   "application/jose+json, application/json, application/jose",
				MaxBytes: revocationMaxBytes,
				Header:   h,
			})
			if err != nil {
				return nil, err
			}
			return &RevocationFetchResult{Status: r.Status, Body: r.Body, ETag: r.Header.Get("ETag"), LastModified: r.Header.Get("Last-Modified")}, nil
		}
	}
	return c, nil
}

// CheckWBAKey implements WBARevocationChecker. It runs in the
// authorization layer, after verification:
//   - RevocationRevoked when the retained list revokes keyID;
//   - RevocationCurrent when a list within next_update plus the grace
//     does not;
//   - RevocationNoList when the origin serves no list (404 or 410) and no
//     list has been retained for it;
//   - RevocationUnavailable when the History cannot be read, so the
//     revocations retained for the origin are unknown;
//   - RevocationStale otherwise: the list cannot be read or verified, it is
//     past next_update plus the grace, the History cannot store it, or a
//     discovery limit refused the fetch. A previously retained or verified
//     list keeps revoking its keys whatever the state.
func (c *OriginRevocationChecker) CheckWBAKey(ctx context.Context, origin, keyID string) (RevocationState, error) {
	l, err := c.retained(ctx, origin)
	if err != nil {
		return RevocationUnavailable, err
	}
	if l != nil && l.IsRevoked(keyID) {
		return RevocationRevoked, nil
	}
	state, err := c.admit(ctx, origin)
	if err != nil {
		switch {
		case errors.Is(err, errNoRevocationList):
			return RevocationNoList, err
		case errors.Is(err, errRevocationStore):
			return RevocationUnavailable, err
		}
		return RevocationStale, err
	}
	l, err = state.fresh(ctx, c)
	if err != nil {
		state.mu.Lock()
		retained := state.current
		state.mu.Unlock()
		if retained != nil && retained.IsRevoked(keyID) {
			return RevocationRevoked, nil
		}
		return RevocationStale, err
	}
	if l.IsRevoked(keyID) {
		return RevocationRevoked, nil
	}
	return RevocationCurrent, nil
}

// Details returns the retained revocation detail for (origin, keyID), for
// assessing compromise exposure with RevocationDetails.Exposes.
func (c *OriginRevocationChecker) Details(origin, keyID string) (RevocationDetails, bool) {
	l, _ := c.retained(context.Background(), origin)
	if l == nil || !l.IsRevoked(keyID) {
		return RevocationDetails{}, false
	}
	return l.Details[keyID], true
}

// RevokedKids returns the sorted kids retained as revoked for origin.
func (c *OriginRevocationChecker) RevokedKids(origin string) []string {
	l, _ := c.retained(context.Background(), origin)
	if l == nil {
		return nil
	}
	out := make([]string, 0, len(l.RevokedKids))
	for k := range l.RevokedKids {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// retained returns the revocations held for origin: the admitted state's
// list, or the History's. A History read failure is reported, never
// treated as "nothing retained".
func (c *OriginRevocationChecker) retained(ctx context.Context, origin string) (*RevocationList, error) {
	origin, err := webBotAuthOrigin(origin)
	if err != nil {
		return nil, nil
	}
	c.mu.Lock()
	state := c.origins[origin]
	c.mu.Unlock()
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.current, nil
	}
	l, err := c.history.LoadRevocations(ctx, origin)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRevocationStore, err)
	}
	return l, nil
}

// operatorBucket is the quota group of an origin: its registrable domain,
// with every tenant of a hosting platform's PRIVATE suffix counted under
// the platform (psl.QuotaKey).
func operatorBucket(origin string) string {
	if d, ok := psl.QuotaKey(origin); ok {
		return d
	}
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return origin
}

func (c *OriginRevocationChecker) admit(ctx context.Context, origin string) (*originRevocations, error) {
	origin, err := webBotAuthOrigin(origin)
	if err != nil {
		return nil, fmt.Errorf("signing: invalid signer origin: %w", err)
	}
	for {
		c.mu.Lock()
		if state := c.origins[origin]; state != nil {
			c.lru.MoveToFront(state.elem)
			c.mu.Unlock()
			return state, nil
		}
		now := c.clock()
		if f, ok := c.failures[origin]; ok && now.Before(f.until) {
			c.mu.Unlock()
			if f.noList {
				return nil, errNoRevocationList
			}
			return nil, errors.New("signing: origin revocation discovery is cooling down")
		}
		call := c.pending[origin]
		if call == nil {
			if len(c.pending) >= c.maxOrigins {
				c.mu.Unlock()
				return nil, errors.New("signing: origin revocation discovery capacity exceeded")
			}
			op := operatorBucket(origin)
			n := 0
			for o := range c.pending {
				if operatorBucket(o) == op {
					n++
				}
			}
			if n >= c.maxPerOperator {
				c.mu.Unlock()
				return nil, errors.New("signing: operator revocation discovery capacity exceeded")
			}
			call = &revocationCall{done: make(chan struct{})}
			c.pending[origin] = call
			go c.discover(context.WithoutCancel(ctx), origin, call)
		}
		c.mu.Unlock()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.err != nil {
			return nil, call.err
		}
	}
}

// discover admits an origin once a signed, fresh list verifies, seeded
// with the history retained for it. A failure costs a 60 s cooldown entry,
// never an admission slot.
func (c *OriginRevocationChecker) discover(ctx context.Context, origin string, call *revocationCall) {
	defer close(call.done)
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	state := &originRevocations{origin: origin}
	hist, err := c.history.LoadRevocations(ctx, origin)
	if err != nil {
		err = fmt.Errorf("%w: %w", errRevocationStore, err)
	} else if hist != nil {
		state.current = hist
		state.interval = min(hist.NextUpdate.Sub(hist.Updated), revocationMaxInterval)
	}
	if err == nil {
		_, err = state.fresh(ctx, c)
	}
	// A verified list the History could not store is admitted anyway: it
	// must keep revoking its keys, and fresh keeps the origin stale until a
	// store succeeds.
	state.mu.Lock()
	keep := err != nil && state.unpersisted != nil && state.current != nil
	state.mu.Unlock()
	if keep {
		err = nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pending, origin)
	if err != nil {
		// No list and nothing retained: the origin publishes none. With a
		// retained list, a 404 is a failure to read it, never a reset.
		noList := errors.Is(err, errNoRevocationList) && state.current == nil
		c.failures[origin] = revocationFailure{until: c.clock().Add(discoveryNegativeTTL), noList: noList}
		if len(c.failures) > c.maxOrigins {
			c.pruneFailures()
		}
		if noList {
			call.err = errNoRevocationList
		} else {
			call.err = fmt.Errorf("signing: origin revocation list unavailable: %w", err)
		}
		return
	}
	delete(c.failures, origin)
	c.evictFor(origin)
	state.elem = c.lru.PushFront(origin)
	c.origins[origin] = state
}

// evictFor makes room for origin: the operator's least recently used
// origin when the operator is at its quota, then the globally least
// recently used origin when the checker is full. Evicted revocations stay
// in History. Called with mu held.
func (c *OriginRevocationChecker) evictFor(origin string) {
	op := operatorBucket(origin)
	var sameOp []*list.Element
	for e := c.lru.Back(); e != nil; e = e.Prev() {
		if operatorBucket(e.Value.(string)) == op {
			sameOp = append(sameOp, e)
		}
	}
	for i := 0; len(sameOp)-i >= c.maxPerOperator; i++ {
		c.evict(sameOp[i])
	}
	for c.lru.Len() >= c.maxOrigins {
		c.evict(c.lru.Back())
	}
}

func (c *OriginRevocationChecker) evict(e *list.Element) {
	origin := e.Value.(string)
	c.lru.Remove(e)
	delete(c.origins, origin)
}

// pruneFailures drops expired cooldowns, then the soonest-expiring, to keep
// the negative cache bounded. Called with mu held.
func (c *OriginRevocationChecker) pruneFailures() {
	now := c.clock()
	for o, f := range c.failures {
		if !now.Before(f.until) {
			delete(c.failures, o)
		}
	}
	for len(c.failures) > c.maxOrigins {
		var oldest string
		var at time.Time
		for o, f := range c.failures {
			if oldest == "" || f.until.Before(at) {
				oldest, at = o, f.until
			}
		}
		delete(c.failures, oldest)
	}
}

// fresh returns a list within next_update, or within the grace window
// after it, starting at most one refresh per 60 s. The refresh is shared by
// every caller and detached from any one caller's context.
func (s *originRevocations) fresh(ctx context.Context, c *OriginRevocationChecker) (*RevocationList, error) {
	s.mu.Lock()
	now := c.clock()
	if s.current != nil && s.unpersisted == nil && now.Before(s.current.NextUpdate) {
		l := s.current
		s.mu.Unlock()
		return l, nil
	}
	call := s.refreshing
	if call == nil && (s.current == nil || now.Sub(s.lastAttempt) >= revocationMinInterval) {
		call = &revocationCall{done: make(chan struct{})}
		s.refreshing = call
		s.lastAttempt = now
		inm, ims := s.etag, s.lastModified
		if s.current == nil {
			inm, ims = "", ""
		}
		go s.refresh(context.WithoutCancel(ctx), c, call, inm, ims)
	}
	s.mu.Unlock()
	var callErr error
	if call != nil {
		select {
		case <-call.done:
			callErr = call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now = c.clock()
	if s.unpersisted != nil {
		return nil, s.unpersisted
	}
	if s.current == nil {
		if callErr == nil {
			callErr = errors.New("revocation list unavailable")
		}
		return nil, callErr
	}
	if now.Before(s.current.NextUpdate) {
		return s.current, nil
	}
	grace := time.Duration(float64(s.interval) * c.grace)
	if s.interval == 0 || !now.Before(s.current.NextUpdate.Add(grace)) {
		if callErr == nil {
			callErr = errors.New("refresh did not extend signed next_update")
		}
		return nil, fmt.Errorf("revocation list past next_update plus grace: %w", callErr)
	}
	return s.current, nil
}

// refresh fetches and verifies the list without holding s.mu, then commits
// it under the lock.
func (s *originRevocations) refresh(ctx context.Context, c *OriginRevocationChecker, call *revocationCall, inm, ims string) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	l, res, err := s.fetchList(ctx, c, inm, ims)
	s.mu.Lock()
	if err == nil && l != nil {
		err = s.commit(c, l, res)
	}
	s.refreshing = nil
	committed := s.current
	retry := s.unpersisted != nil
	s.mu.Unlock()
	if err == nil && (l != nil || retry) && committed != nil {
		herr := c.history.StoreRevocations(ctx, s.origin, committed)
		s.mu.Lock()
		if herr != nil {
			s.unpersisted = fmt.Errorf("revocation history store failed: %w", herr)
			err = s.unpersisted
		} else {
			s.unpersisted = nil
		}
		s.mu.Unlock()
	}
	call.err = err
	close(call.done)
}

// fetchList returns (nil, nil, nil) for a 304, which authenticates nothing.
func (s *originRevocations) fetchList(ctx context.Context, c *OriginRevocationChecker, inm, ims string) (*RevocationList, *RevocationFetchResult, error) {
	res, err := c.fetch(ctx, s.origin+RevocationListPath, inm, ims)
	if err != nil {
		return nil, nil, err
	}
	if res.Status == http.StatusNotModified && inm+ims != "" {
		return nil, nil, nil
	}
	if res.Status == http.StatusNotFound || res.Status == http.StatusGone {
		return nil, nil, errNoRevocationList
	}
	if res.Status != http.StatusOK {
		return nil, nil, fmt.Errorf("revocation list returned HTTP %d", res.Status)
	}
	payload, err := verifyRevocationJWS(res.Body, func(kid string) (*JWK, error) {
		return c.keys.ResolveWBAKey(ctx, s.origin, kid)
	})
	if err != nil {
		return nil, nil, err
	}
	l, err := ParseRevocationList(payload, s.origin)
	if err != nil {
		return nil, nil, err
	}
	return retainableForWBA(l), res, nil
}

// retainableForWBA keeps only what a Web Bot Auth check can use: kids
// shaped like RFC 7638 thumbprints, and their details. jtis are not used by
// transport revocation.
func retainableForWBA(l *RevocationList) *RevocationList {
	for kid := range l.RevokedKids {
		if !isThumbprint(kid) {
			delete(l.RevokedKids, kid)
			delete(l.Details, kid)
		}
	}
	l.RevokedJTIs = map[string]struct{}{}
	return l
}

func isThumbprint(s string) bool {
	if len(s) != wbaThumbprintLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isAlpha(c) && !isDigit(c) && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// commit applies the monotonicity rules. Called with s.mu held.
func (s *originRevocations) commit(c *OriginRevocationChecker, l *RevocationList, res *RevocationFetchResult) error {
	now := c.clock()
	if l.Updated.After(now.Add(revocationClockSkew)) {
		return errors.New("revocation list updated is in the future")
	}
	if s.current != nil {
		if l.Updated.Before(s.current.Updated) {
			return errors.New("revocation list is older than the retained list; refusing to roll back")
		}
		if err := l.Retain(s.current); err != nil {
			return err
		}
	}
	if len(l.RevokedKids) > maxRetainedRevocations {
		return errors.New("revocation list exceeds the retained-entry bound")
	}
	s.current = l
	s.etag = res.ETag
	if len(res.LastModified) <= 64 && !strings.ContainsAny(res.LastModified, "\r\n") {
		s.lastModified = res.LastModified
	}
	s.interval = min(l.NextUpdate.Sub(l.Updated), revocationMaxInterval)
	return nil
}

// memoryRevocationHistory is the default RevocationHistory. It refuses
// rather than evicts: a quota group (psl.QuotaKey: an operator, with every
// tenant of a hosting platform's PRIVATE suffix counted together) holds at
// most historyOriginsPerOperator origins and historyKidsPerOperator kids,
// and the table at most limit origins and historyKidBudget kids. A store
// that does not fit fails, so that origin's state is RevocationStale (it
// cannot spend) instead of another origin's revocations being forgotten. It lives in memory: public deployments must supply a durable
// History so revocations hold across restarts and replicas.
type memoryRevocationHistory struct {
	mu        sync.Mutex
	limit     int // origins
	budget    int // retained kids across all origins
	opLimit   int // origins per quota group
	opBudget  int // retained kids per quota group
	kids      int
	lists     map[string]*historyEntry
	opKids    map[string]int
	opOrigins map[string]int
}

type historyEntry struct {
	group string
	list  *RevocationList
}

// errHistoryFull refuses a store that would exceed a quota.
var errHistoryFull = errors.New("signing: revocation history quota exceeded")

const (
	historyOriginsPerOperator = 32
	historyKidsPerOperator    = 8 * maxRetainedRevocations
)

func newMemoryRevocationHistory(limit int) *memoryRevocationHistory {
	return &memoryRevocationHistory{
		limit: limit, budget: historyKidBudget,
		opLimit: historyOriginsPerOperator, opBudget: historyKidsPerOperator,
		lists: map[string]*historyEntry{}, opKids: map[string]int{}, opOrigins: map[string]int{},
	}
}

func (h *memoryRevocationHistory) LoadRevocations(_ context.Context, origin string) (*RevocationList, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.lists[origin]; ok {
		return e.list, nil
	}
	return nil, nil
}

// StoreRevocations merges l into what is retained for origin: never
// withdrawing a revocation or weakening a detail, and keeping the newer
// updated time, so a late write from an older refresh cannot roll the
// history back. A list that revokes nothing is not stored.
func (h *memoryRevocationHistory) StoreRevocations(_ context.Context, origin string, l *RevocationList) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	old, exists := h.lists[origin]
	if !exists && len(l.RevokedKids) == 0 {
		return nil
	}
	merged := cloneRevocationList(l)
	oldKids := 0
	group := operatorBucket(origin)
	if exists {
		oldKids = len(old.list.RevokedKids)
		group = old.group
		if l.Updated.Before(old.list.Updated) {
			merged = cloneRevocationList(old.list)
			if err := merged.Retain(l); err != nil {
				return err
			}
		} else if err := merged.Retain(old.list); err != nil {
			return err
		}
	}
	added := len(merged.RevokedKids) - oldKids
	if (!exists && (h.opOrigins[group] >= h.opLimit || len(h.lists) >= h.limit)) ||
		h.opKids[group]+added > h.opBudget || h.kids+added > h.budget {
		return errHistoryFull
	}
	if !exists {
		h.opOrigins[group]++
	}
	h.lists[origin] = &historyEntry{group: group, list: merged}
	h.kids += added
	h.opKids[group] += added
	return nil
}

func cloneRevocationList(l *RevocationList) *RevocationList {
	c := *l
	c.RevokedKids = maps.Clone(l.RevokedKids)
	c.RevokedJTIs = maps.Clone(l.RevokedJTIs)
	c.Details = maps.Clone(l.Details)
	if c.RevokedKids == nil {
		c.RevokedKids = map[string]struct{}{}
	}
	if c.RevokedJTIs == nil {
		c.RevokedJTIs = map[string]struct{}{}
	}
	if c.Details == nil {
		c.Details = map[string]RevocationDetails{}
	}
	return &c
}
