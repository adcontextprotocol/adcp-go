package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/signing"
	"github.com/stretchr/testify/require"
)

// The JWS fixtures model R4's three artifacts:
//   - gov.vendor-7k3q.com, a governance agent listed by vendor-7k3q.com;
//   - legacy-7k3q.com, an operator that has never published a trust record;
//   - down-7k3q.com, an operator whose trust record cannot be read.
const (
	govOrigin = "https://gov.vendor-7k3q.com"
	govTyp    = "adcp-gov+jws"
)

type jwsRig struct {
	t          *testing.T
	trust      *evalRig
	key        ed25519.PrivateKey
	kid        string
	mu         sync.Mutex
	dirs       map[string]*signing.DirectoryResponse
	dirErrs    map[string]error
	revoked    map[string]string // origin -> revoked kid
	revErr     map[string]bool
	archive    map[string]string // origin -> archive body
	archiveErr map[string]error
	v          *JWSVerifier
}

func newJWSRig(t *testing.T) *jwsRig {
	t.Helper()
	r := &jwsRig{t: t, trust: newEvalRig(t), dirs: map[string]*signing.DirectoryResponse{}, dirErrs: map[string]error{},
		revoked: map[string]string{}, revErr: map[string]bool{}, archive: map[string]string{}, archiveErr: map[string]error{}}
	r.key, r.kid = newEd25519(t)
	r.trust.server.set(trustURL("vendor-7k3q.com"), 200, `{"agents":[{"url":"https://gov.vendor-7k3q.com/","roles":["adcp:governance"]}]}`)
	r.trust.server.fail(trustURL("down-7k3q.com"), errors.New("timeout"))
	r.setDirectory(govOrigin, r.key)

	keys := signing.NewDirectoryResolver(signing.DirectoryResolverOptions{
		Clock: func() time.Time { return testNow },
		Fetch: func(_ context.Context, url string) (*signing.DirectoryResponse, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			origin := strings.TrimSuffix(url, signing.DirectoryPath)
			if err := r.dirErrs[origin]; err != nil {
				return nil, err
			}
			if d := r.dirs[origin]; d != nil {
				return d, nil
			}
			return &signing.DirectoryResponse{Status: 404}, nil
		},
	})
	revocation, err := signing.NewOriginRevocationChecker(signing.OriginRevocationCheckerOptions{
		Keys:  keys,
		Clock: func() time.Time { return testNow },
		Fetch: func(_ context.Context, url, _, _ string) (*signing.RevocationFetchResult, error) {
			origin := strings.TrimSuffix(url, signing.RevocationListPath)
			r.mu.Lock()
			fail, kid := r.revErr[origin], r.revoked[origin]
			r.mu.Unlock()
			if fail {
				return nil, errors.New("timeout")
			}
			return &signing.RevocationFetchResult{Status: 200, Body: []byte(r.revocationList(origin, kid))}, nil
		},
	})
	require.NoError(t, err)
	archive := &HTTPGovernanceKeyArchive{Fetch: func(_ context.Context, url string) (*TrustResponse, error) {
		origin := strings.TrimSuffix(url, GovernanceKeyArchivePath)
		r.mu.Lock()
		defer r.mu.Unlock()
		if err := r.archiveErr[origin]; err != nil {
			return nil, err
		}
		if body, ok := r.archive[origin]; ok {
			return &TrustResponse{Status: 200, Body: []byte(body)}, nil
		}
		return &TrustResponse{Status: 404}, nil
	}}
	r.v = &JWSVerifier{Records: r.trust.eval, Keys: keys, Revocation: revocation, Archive: archive}
	return r
}

func newEd25519(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	jwk, err := signing.PublicDirectoryKey(priv)
	require.NoError(t, err)
	tp, err := signing.JWKThumbprint(jwk)
	require.NoError(t, err)
	return priv, tp
}

func (r *jwsRig) setDirectory(origin string, keys ...ed25519.PrivateKey) {
	r.t.Helper()
	anyKeys := make([]any, len(keys))
	for i, k := range keys {
		anyKeys[i] = k
	}
	d, err := signing.SignKeyDirectory(origin, anyKeys, testNow.Add(-time.Minute), time.Hour)
	require.NoError(r.t, err)
	r.mu.Lock()
	r.dirs[origin] = d
	r.mu.Unlock()
}

func (r *jwsRig) revocationList(origin, revokedKid string) string {
	kids := []string{}
	if revokedKid != "" {
		kids = append(kids, revokedKid)
	}
	payload, _ := json.Marshal(map[string]any{
		"version": 1, "issuer": origin, "updated": testNow.Add(-time.Minute).Format(time.RFC3339),
		"next_update": testNow.Add(5 * time.Minute).Format(time.RFC3339), "revoked_kids": kids, "revoked_jtis": []string{},
	})
	return compactJWS(r.key, map[string]any{"alg": "EdDSA", "typ": signing.RevocationListTyp, "kid": r.kid}, payload)
}

func compactJWS(key ed25519.PrivateKey, header map[string]any, payload []byte) string {
	h, _ := json.Marshal(header)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func govToken(key ed25519.PrivateKey, kid, iss string) string {
	return compactJWS(key, map[string]any{"alg": "EdDSA", "typ": govTyp, "kid": kid}, []byte(`{"iss":"`+iss+`","sub":"s","jti":"j"}`))
}

func govOpts() JWSVerifyOptions {
	return JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: govOrigin + "/tenant/acme", Typ: govTyp}
}

func requireJWSError(t *testing.T, err error, unavailable, revoked bool) {
	t.Helper()
	var je *JWSError
	require.ErrorAs(t, err, &je)
	require.Equal(t, unavailable, je.Unavailable, je.Error())
	require.Equal(t, revoked, je.Revoked, je.Error())
	require.NotErrorIs(t, err, ErrJWSProfile32, "a refusal never selects the 3.2 profile")
}

// TestJWSSelection pins R4's selection: the trust record derived from O
// decides, and only a record that does not list O selects the 3.2 profile.
func TestJWSSelection(t *testing.T) {
	r := newJWSRig(t)
	r.trust.server.set(trustURL("other-7k3q.com"), 200, `{"agents":[{"url":"https://sales.other-7k3q.com/mcp","roles":["adcp:sales"]}]}`)
	ctx := context.Background()
	for _, tc := range []struct {
		name, url string
		want      JWSProfile
		outage    bool
	}{
		{"a record that lists O, with any role, selects the key directory", govOrigin + "/tenant/acme", JWSProfileWBA, false},
		{"a record that does not list O selects 3.2", "https://gov.other-7k3q.com", JWSProfile32, false},
		{"an operator that never published a record selects 3.2", "https://gov.legacy-7k3q.com", JWSProfile32, false},
		{"an O with no registrable domain selects 3.2", "https://github.io", JWSProfile32, false},
		{"a record that cannot be read is not absence", "https://gov.down-7k3q.com", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := r.v.SelectProfile(ctx, tc.url)
			if tc.outage {
				requireJWSError(t, err, true, false)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, sel.Profile)
			require.False(t, sel.StaleTrustRecord)
		})
	}
	_, err := r.v.SelectProfile(ctx, "http://gov.vendor-7k3q.com")
	requireJWSError(t, err, false, false)
}

func TestJWSVerifyOnTheKeyDirectory(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()
	tok := govToken(r.key, r.kid, govOrigin+"/tenant/acme")
	got, err := r.v.Verify(ctx, tok, govOpts())
	require.NoError(t, err)
	require.Equal(t, govOrigin, got.Origin)
	require.Equal(t, r.kid, got.KeyID)
	require.Equal(t, signing.RevocationCurrent, got.Revocation)
	require.False(t, got.Archived)
	require.JSONEq(t, `{"iss":"https://gov.vendor-7k3q.com/tenant/acme","sub":"s","jti":"j"}`, string(got.Payload))

	// The other two artifacts take O from the URL the caller names and do
	// not read iss.
	for _, art := range []JWSArtifact{DesignatedResponse, RightsAttestation} {
		tok := compactJWS(r.key, map[string]any{"alg": "EdDSA", "typ": "adcp-other+jws", "kid": r.kid}, []byte(`{"x":1}`))
		_, err := r.v.Verify(ctx, tok, JWSVerifyOptions{Artifact: art, SignerURL: govOrigin + "/mcp", Typ: "adcp-other+jws"})
		require.NoError(t, err, art)
	}

	// No key-purpose checks: a directory key carries no key_ops or
	// adcp_use, and none is required (purpose separation is by origin).
	jwk, err := r.v.Keys.ResolveWBAKey(ctx, govOrigin, r.kid)
	require.NoError(t, err)
	require.Empty(t, jwk.KeyOps)
	require.Empty(t, jwk.AdcpUse)
}

func TestJWSFallbackTo32(t *testing.T) {
	r := newJWSRig(t)
	other, otherKid := newEd25519(t)
	// The 3.2 issuer has a WBA-shaped token and even a directory: selection
	// still reads only the trust record.
	r.setDirectory("https://gov.legacy-7k3q.com", other)
	tok := govToken(other, otherKid, "https://gov.legacy-7k3q.com")
	_, err := r.v.Verify(context.Background(), tok, JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: "https://gov.legacy-7k3q.com", Typ: govTyp})
	require.ErrorIs(t, err, ErrJWSProfile32)
}

type boundOrigins map[string]bool

func (b boundOrigins) PrincipalForOrigin(_ context.Context, origin string) (string, bool, error) {
	if b == nil {
		return "", false, errors.New("binding store down")
	}
	return "p", b[origin], nil
}

// TestJWSNoDowngradeForBoundOrigins pins R4's no downgrade: an origin the
// verifier has bound is refused instead of being sent to the 3.2 profile,
// for example after its operator delists it.
func TestJWSNoDowngradeForBoundOrigins(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()
	r.v.BoundOrigins = boundOrigins{"https://gov.legacy-7k3q.com": true}
	_, err := r.v.Verify(ctx, govToken(r.key, r.kid, "https://gov.legacy-7k3q.com"), JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: "https://gov.legacy-7k3q.com", Typ: govTyp})
	requireJWSError(t, err, false, false)
	require.ErrorContains(t, err, "bound")

	// An unbound origin still falls back.
	sel, err := r.v.SelectProfile(ctx, "https://other.legacy-7k3q.com")
	require.NoError(t, err)
	require.Equal(t, JWSProfile32, sel.Profile)

	// A binding store that fails refuses rather than downgrading.
	r.v.BoundOrigins = boundOrigins(nil)
	_, err = r.v.SelectProfile(ctx, "https://gov.legacy-7k3q.com")
	requireJWSError(t, err, true, false)
	// A listed origin never consults the store.
	sel, err = r.v.SelectProfile(ctx, govOrigin)
	require.NoError(t, err)
	require.Equal(t, JWSProfileWBA, sel.Profile)
}

func TestJWSFetchFailureIsNotAbsence(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()

	// The trust record cannot be read.
	tok := govToken(r.key, r.kid, "https://gov.down-7k3q.com")
	_, err := r.v.Verify(ctx, tok, JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: "https://gov.down-7k3q.com", Typ: govTyp})
	requireJWSError(t, err, true, false)

	// A record that was read and listed O, then fails: within the
	// OutageContinuation bound the WBA path is still selected, never 3.2,
	// and the result says the record is stale.
	flaky := "https://gov.flaky-7k3q.com"
	r.trust.server.set(trustURL("flaky-7k3q.com"), 200, `{"agents":[{"url":"https://gov.flaky-7k3q.com","roles":["adcp:governance"]}]}`)
	r.setDirectory(flaky, r.key)
	got, err := r.v.Verify(ctx, govToken(r.key, r.kid, flaky), JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: flaky, Typ: govTyp})
	require.NoError(t, err)
	require.False(t, got.StaleTrustRecord)
	r.trust.server.fail(trustURL("flaky-7k3q.com"), errors.New("timeout"))
	r.trust.now = r.trust.now.Add(2 * time.Minute)
	got, err = r.v.Verify(ctx, govToken(r.key, r.kid, flaky), JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: flaky, Typ: govTyp})
	require.NoError(t, err)
	require.True(t, got.StaleTrustRecord, "a spend-committing surface must be able to refuse")
	// A 404 after a record was published is a failure, not absence.
	r.trust.server.set(trustURL("flaky-7k3q.com"), 404, "")
	r.trust.now = r.trust.now.Add(2 * time.Minute)
	sel, err := r.v.SelectProfile(ctx, flaky)
	require.NoError(t, err)
	require.Equal(t, JWSProfileWBA, sel.Profile)
	require.True(t, sel.StaleTrustRecord)
	// Past the bound, the outage is an error, still never 3.2.
	r.trust.now = r.trust.now.Add(10 * time.Minute)
	_, err = r.v.SelectProfile(ctx, flaky)
	requireJWSError(t, err, true, false)
	// A record read after the outage that no longer lists O selects 3.2:
	// the operator delisted it.
	r.trust.server.set(trustURL("flaky-7k3q.com"), 200, `{"agents":[]}`)
	r.trust.now = r.trust.now.Add(2 * time.Minute)
	sel, err = r.v.SelectProfile(ctx, flaky)
	require.NoError(t, err)
	require.Equal(t, JWSProfile32, sel.Profile)
	// ...but once that record cannot be refreshed, the outage is an error:
	// the last record read never selects 3.2 while the fetch fails.
	r.trust.server.fail(trustURL("flaky-7k3q.com"), errors.New("timeout"))
	r.trust.now = r.trust.now.Add(24 * time.Hour)
	_, err = r.v.SelectProfile(ctx, flaky)
	requireJWSError(t, err, true, false)

	// The key directory cannot be read.
	fresh := newJWSRig(t)
	fresh.mu.Lock()
	fresh.dirErrs[govOrigin] = errors.New("connection refused")
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, govToken(fresh.key, fresh.kid, govOrigin), govOpts())
	requireJWSError(t, err, true, false)

	// A malformed token is refused before anything is fetched.
	before := r.trust.server.count(trustURL("down-7k3q.com"))
	_, err = r.v.Verify(ctx, "not.a.jws", JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: "https://gov.down-7k3q.com", Typ: govTyp})
	requireJWSError(t, err, false, false)
	require.Equal(t, before, r.trust.server.count(trustURL("down-7k3q.com")))
}

func TestJWSRevocation(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()
	r.mu.Lock()
	r.revoked[govOrigin] = r.kid
	r.mu.Unlock()
	_, err := r.v.Verify(ctx, govToken(r.key, r.kid, govOrigin), govOpts())
	requireJWSError(t, err, false, true)

	// Another key of the same origin is not revoked.
	r = newJWSRig(t)
	second, secondKid := newEd25519(t)
	r.setDirectory(govOrigin, r.key, second)
	r.mu.Lock()
	r.revoked[govOrigin] = r.kid
	r.mu.Unlock()
	got, err := r.v.Verify(ctx, govToken(second, secondKid, govOrigin), govOpts())
	require.NoError(t, err)
	require.Equal(t, signing.RevocationCurrent, got.Revocation)

	// A list that cannot be read is refused as unavailable by default, and
	// accepted only when the surface allows a stale list.
	r = newJWSRig(t)
	r.mu.Lock()
	r.revErr[govOrigin] = true
	r.mu.Unlock()
	_, err = r.v.Verify(ctx, govToken(r.key, r.kid, govOrigin), govOpts())
	requireJWSError(t, err, true, false)
	opts := govOpts()
	opts.AcceptStaleRevocation = true
	got, err = r.v.Verify(ctx, govToken(r.key, r.kid, govOrigin), opts)
	require.NoError(t, err)
	require.Equal(t, signing.RevocationStale, got.Revocation)
}

func TestJWSAlgAndKid(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()
	other, otherKid := newEd25519(t)
	payload := []byte(`{"iss":"https://gov.vendor-7k3q.com"}`)
	sign := func(h map[string]any) string { return compactJWS(r.key, h, payload) }
	hdr := func(extra map[string]any) map[string]any {
		h := map[string]any{"alg": "EdDSA", "typ": govTyp, "kid": r.kid}
		for k, v := range extra {
			if v == nil {
				delete(h, k)
			} else {
				h[k] = v
			}
		}
		return h
	}
	for _, tc := range []struct{ name, tok, reason string }{
		{"alg Ed25519 (the 3.2 vectors' spelling)", sign(hdr(map[string]any{"alg": "Ed25519"})), "alg is not EdDSA"},
		{"alg ES256", sign(hdr(map[string]any{"alg": "ES256"})), "alg is not EdDSA"},
		{"alg none", sign(hdr(map[string]any{"alg": "none"})), "alg is not EdDSA"},
		{"no alg", sign(hdr(map[string]any{"alg": nil})), "alg is not EdDSA"},
		{"kid is not a thumbprint", sign(hdr(map[string]any{"kid": "gov-2026-03"})), "kid is not a JWK thumbprint"},
		{"no kid", sign(hdr(map[string]any{"kid": nil})), "kid is not a JWK thumbprint"},
		{"kid of a key O does not publish", compactJWS(other, map[string]any{"alg": "EdDSA", "typ": govTyp, "kid": otherKid}, payload), "not in the issuer's key directory"},
		{"kid of O's key, signed by another key", compactJWS(other, hdr(nil), payload), "signature did not verify"},
		{"typ normalized", sign(hdr(map[string]any{"typ": "adcp-gov"})), "typ mismatch"},
		{"unknown crit", sign(hdr(map[string]any{"crit": []string{"authorized_task"}, "authorized_task": true})), "not understood"},
		{"b64 false", sign(hdr(map[string]any{"b64": false})), "b64"},
		{"b64 true", sign(hdr(map[string]any{"b64": true})), "b64"},
		{"empty payload", strings.Replace(sign(hdr(nil)), "."+base64.RawURLEncoding.EncodeToString(payload)+".", "..", 1), "not a compact JWS"},
		{"payload that is not strict JSON", compactJWS(r.key, hdr(nil), []byte(`{"iss":"https://gov.vendor-7k3q.com","ISS":"x"}`)), "payload is not strict JSON"},
		{"header members that differ only in case", dupHeader(r.key, r.kid, payload), "strict JSON"},
		{"iss at another origin", compactJWS(r.key, hdr(nil), []byte(`{"iss":"https://gov.other-7k3q.com"}`)), "iss is not at the issuer's origin"},
		{"no iss", compactJWS(r.key, hdr(nil), []byte(`{}`)), "payload has no iss"},
		{"iss spelled ISS", compactJWS(r.key, hdr(nil), []byte(`{"ISS":"https://gov.vendor-7k3q.com"}`)), "payload has no iss"},
		{"duplicate iss in payload", compactJWS(r.key, hdr(nil), []byte(`{"iss":"https://gov.vendor-7k3q.com","iss":"https://x.example"}`)), "payload is not strict JSON"},
		{"not compact", "a.b", "not a compact JWS"},
		{"padded segment", sign(hdr(nil)) + "=", "canonical base64url"},
		{"oversized", strings.Repeat("a", jwsMaxBytes+1), "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.v.Verify(ctx, tc.tok, govOpts())
			requireJWSError(t, err, false, false)
			require.ErrorContains(t, err, tc.reason)
		})
	}
	// A crit extension the caller processes is accepted.
	opts := govOpts()
	opts.Crit = []string{"authorized_task"}
	_, err := r.v.Verify(ctx, sign(map[string]any{"alg": "EdDSA", "typ": govTyp, "kid": r.kid, "crit": []string{"authorized_task"}, "authorized_task": true}), opts)
	require.NoError(t, err)
	_, err = r.v.Verify(ctx, sign(map[string]any{"alg": "EdDSA", "typ": govTyp, "kid": r.kid, "crit": []string{"authorized_task"}}), opts)
	requireJWSError(t, err, false, false)
}

func dupHeader(key ed25519.PrivateKey, kid string, payload []byte) string {
	h := `{"alg":"EdDSA","typ":"` + govTyp + `","kid":"` + kid + `","ALG":"none"}`
	input := base64.RawURLEncoding.EncodeToString([]byte(h)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

// TestJWSHistory pins historical verification of a governance context: a
// key that left O's directory is found in the governance key archive at O,
// and only when the caller asks for it.
func TestJWSHistory(t *testing.T) {
	r := newJWSRig(t)
	ctx := context.Background()
	retired, retiredKid := newEd25519(t)
	retiredJWK, err := signing.PublicDirectoryKey(retired)
	require.NoError(t, err)
	retiredJWK.Kid = retiredKid
	entry, _ := json.Marshal(retiredJWK)
	r.mu.Lock()
	r.archive[govOrigin] = `{"keys":[` + string(entry) + `]}`
	r.mu.Unlock()
	tok := govToken(retired, retiredKid, govOrigin)

	_, err = r.v.Verify(ctx, tok, govOpts())
	requireJWSError(t, err, false, false)

	hist := govOpts()
	hist.Historical = true
	got, err := r.v.Verify(ctx, tok, hist)
	require.NoError(t, err)
	require.True(t, got.Archived)
	require.Equal(t, retiredKid, got.KeyID)

	// A current key still verifies from the directory.
	got, err = r.v.Verify(ctx, govToken(r.key, r.kid, govOrigin), hist)
	require.NoError(t, err)
	require.False(t, got.Archived)

	// A revoked archived key is refused: revoked_kids covers every token
	// ever signed under the key.
	fresh := newJWSRig(t)
	fresh.mu.Lock()
	fresh.archive[govOrigin] = `{"keys":[` + string(entry) + `]}`
	fresh.revoked[govOrigin] = retiredKid
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, false, true)

	// The archive cannot be read: an outage, not a refusal of the key.
	fresh = newJWSRig(t)
	fresh.mu.Lock()
	fresh.archiveErr[govOrigin] = errors.New("timeout")
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, true, false)

	// An archive that carries private key material fails whole.
	priv, _ := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "x": retiredJWK.X, "D": "AAAA"})
	fresh = newJWSRig(t)
	fresh.mu.Lock()
	fresh.archive[govOrigin] = `{"keys":[` + string(priv) + `]}`
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, true, false)

	// An entry whose kid is not its thumbprint is skipped: its revocation
	// would be published under that kid, which the origin's revocation
	// list does not retain for Web Bot Auth keys.
	withKid := retiredJWK
	withKid.Kid = "gov-2026-03"
	legacyEntry, _ := json.Marshal(withKid)
	p256 := `{"kty":"EC","crv":"P-256","x":"f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU","y":"x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"}`
	fresh = newJWSRig(t)
	fresh.mu.Lock()
	fresh.archive[govOrigin] = `{"keys":[` + p256 + `,` + string(legacyEntry) + `]}`
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, false, false)
	require.ErrorContains(t, err, "not in the issuer's key directory")
	// An entry with no kid is skipped too.
	noKid := retiredJWK
	noKid.Kid = ""
	noKidEntry, _ := json.Marshal(noKid)
	fresh = newJWSRig(t)
	fresh.mu.Lock()
	fresh.archive[govOrigin] = `{"keys":[` + string(noKidEntry) + `]}`
	fresh.mu.Unlock()
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, false, false)
	// The same key with kid set to its thumbprint, beside a P-256 key the
	// archive skips, verifies.
	withKid.Kid = retiredKid
	thumbEntry, _ := json.Marshal(withKid)
	fresh = newJWSRig(t)
	fresh.mu.Lock()
	fresh.archive[govOrigin] = `{"keys":[` + p256 + `,` + string(thumbEntry) + `]}`
	fresh.mu.Unlock()
	got, err = fresh.v.Verify(ctx, tok, hist)
	require.NoError(t, err)
	require.True(t, got.Archived)

	// An origin that publishes no archive (404): the key is not found, a
	// refusal rather than an outage.
	fresh = newJWSRig(t)
	_, err = fresh.v.Verify(ctx, tok, hist)
	requireJWSError(t, err, false, false)

	// History applies to a governance context only, and needs an archive.
	_, err = r.v.Verify(ctx, tok, JWSVerifyOptions{Artifact: RightsAttestation, SignerURL: govOrigin, Typ: govTyp, Historical: true})
	require.Error(t, err)
	noArchive := *r.v
	noArchive.Archive = nil
	_, err = noArchive.Verify(ctx, tok, hist)
	require.Error(t, err)
}

func TestJWSVerifierConfiguration(t *testing.T) {
	r := newJWSRig(t)
	for _, v := range []*JWSVerifier{{}, {Records: r.v.Records}, {Records: r.v.Records, Keys: r.v.Keys}} {
		_, err := v.Verify(context.Background(), "a.b.c", govOpts())
		require.ErrorIs(t, err, errJWSNotConfigured)
	}
	_, err := r.v.Verify(context.Background(), "a.b.c", JWSVerifyOptions{Artifact: GovernanceContext, SignerURL: govOrigin})
	require.Error(t, err, "Typ is required")
	_, err = r.v.Verify(context.Background(), "a.b.c", JWSVerifyOptions{SignerURL: govOrigin, Typ: govTyp})
	require.Error(t, err, "Artifact is required")
}

func TestGovernanceKeyArchiveCapacity(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	a := &HTTPGovernanceKeyArchive{MaxConcurrent: 1, Fetch: func(ctx context.Context, _ string) (*TrustResponse, error) {
		started <- struct{}{}
		<-release
		return &TrustResponse{Status: 404}, nil
	}}
	done := make(chan error, 2)
	go func() {
		_, err := a.ArchivedKey(context.Background(), "https://a.slow-7k3q.com", "x")
		done <- err
	}()
	<-started
	// One operator has one fetch in flight: another of its origins fails.
	_, err := a.ArchivedKey(context.Background(), "https://b.slow-7k3q.com", "x")
	require.ErrorContains(t, err, "capacity exceeded for operator")
	// Another operator waits for a global slot until its context ends...
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = a.ArchivedKey(ctx, govOrigin, "x")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// ...and gets one when it frees.
	go func() {
		_, err := a.ArchivedKey(context.Background(), govOrigin, "x")
		done <- err
	}()
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
}
