package signing

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wbaTestNow is the fixed wall clock for Web Bot Auth tests.
var wbaTestNow = time.Unix(1_790_841_610, 0)

func wbaClock() time.Time { return wbaTestNow }

func newEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return k
}

func newP256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return k
}

// fakeDirectories serves signed key directories for test origins and
// counts fetches. An origin mapped to an error fails.
type fakeDirectories struct {
	mu      sync.Mutex
	dirs    map[string]*DirectoryResponse
	errs    map[string]error
	fetches map[string]int
	block   map[string]chan struct{}
}

func newFakeDirectories() *fakeDirectories {
	return &fakeDirectories{dirs: map[string]*DirectoryResponse{}, errs: map[string]error{}, fetches: map[string]int{}, block: map[string]chan struct{}{}}
}

func (f *fakeDirectories) publish(t *testing.T, origin string, created time.Time, lifetime time.Duration, keys ...any) {
	t.Helper()
	resp, err := SignKeyDirectory(origin, keys, created, lifetime)
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[origin] = resp
	delete(f.errs, origin)
}

func (f *fakeDirectories) fail(origin string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[origin] = err
}

func (f *fakeDirectories) count(origin string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches[origin]
}

func (f *fakeDirectories) fetch(ctx context.Context, url string) (*DirectoryResponse, error) {
	origin := strings.TrimSuffix(url, DirectoryPath)
	f.mu.Lock()
	f.fetches[origin]++
	block := f.block[origin]
	err := f.errs[origin]
	resp := f.dirs[origin]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return &DirectoryResponse{Status: http.StatusNotFound, Header: http.Header{}}, nil
	}
	return resp, nil
}

// revocationServer builds signed revocation lists for one origin.
type revocationServer struct {
	mu       sync.Mutex
	origin   string
	key      any
	kid      string
	body     []byte
	status   int
	fetches  int
	lastINM  string
	fetchErr error
}

func newRevocationServer(t *testing.T, origin string, key any) *revocationServer {
	t.Helper()
	jwk, err := PublicDirectoryKey(key)
	require.NoError(t, err)
	return &revocationServer{origin: origin, key: key, kid: jwk.Kid, status: http.StatusOK}
}

type revocationPayload struct {
	Version     int                          `json:"version"`
	Issuer      string                       `json:"issuer"`
	Updated     string                       `json:"updated"`
	NextUpdate  string                       `json:"next_update"`
	RevokedKids []string                     `json:"revoked_kids"`
	RevokedJTIs []string                     `json:"revoked_jtis,omitempty"`
	Details     map[string]map[string]string `json:"revocation_details,omitempty"`
}

func signJWS(t *testing.T, key any, header map[string]any, payload []byte) (protected, encodedPayload, signature string) {
	t.Helper()
	hb, err := json.Marshal(header)
	require.NoError(t, err)
	protected = base64.RawURLEncoding.EncodeToString(hb)
	encodedPayload = base64.RawURLEncoding.EncodeToString(payload)
	sig, err := signWithKey(key, []byte(protected+"."+encodedPayload))
	require.NoError(t, err)
	return protected, encodedPayload, base64.RawURLEncoding.EncodeToString(sig)
}

func (s *revocationServer) set(t *testing.T, p revocationPayload) {
	t.Helper()
	if p.Version == 0 {
		p.Version = 1
	}
	if p.Issuer == "" {
		p.Issuer = s.origin
	}
	if p.RevokedKids == nil {
		p.RevokedKids = []string{}
	}
	payload, err := json.Marshal(p)
	require.NoError(t, err)
	alg := "EdDSA"
	if _, ok := s.key.(*ecdsa.PrivateKey); ok {
		alg = "ES256"
	}
	prot, pay, sig := signJWS(t, s.key, map[string]any{"alg": alg, "typ": RevocationListTyp, "kid": s.kid}, payload)
	body, err := json.Marshal(map[string]any{"payload": pay, "signatures": []map[string]string{{"protected": prot, "signature": sig}}})
	require.NoError(t, err)
	s.mu.Lock()
	s.body, s.status = body, http.StatusOK
	s.mu.Unlock()
}

func (s *revocationServer) fetch(_ context.Context, url, inm, _ string) (*RevocationFetchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetches++
	s.lastINM = inm
	if s.fetchErr != nil {
		return nil, s.fetchErr
	}
	if url != s.origin+RevocationListPath {
		return nil, errors.New("unexpected revocation URL " + url)
	}
	return &RevocationFetchResult{Status: s.status, Body: s.body, ETag: `"v1"`}, nil
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// wbaRig is a buyer origin with a published directory and revocation list,
// and a verifier configured against them.
type wbaRig struct {
	origin   string
	key      ed25519.PrivateKey
	dirs     *fakeDirectories
	resolver *DirectoryResolver
	revs     *revocationServer
	checker  *OriginRevocationChecker
	replay   *MemoryReplayStore
}

func newWBARig(t *testing.T) *wbaRig {
	t.Helper()
	rig := &wbaRig{origin: "https://buyer.example.com", key: newEd25519(t), dirs: newFakeDirectories(), replay: NewMemoryReplayStore(0)}
	rig.dirs.publish(t, rig.origin, wbaTestNow.Add(-time.Hour), 24*time.Hour, rig.key)
	rig.resolver = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: wbaClock})
	rig.revs = newRevocationServer(t, rig.origin, rig.key)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(wbaTestNow.Add(-time.Minute)), NextUpdate: rfc3339(wbaTestNow.Add(10 * time.Minute))})
	var err error
	rig.checker, err = NewOriginRevocationChecker(OriginRevocationCheckerOptions{Keys: rig.resolver, Fetch: rig.revs.fetch, Clock: wbaClock})
	require.NoError(t, err)
	return rig
}

func (rig *wbaRig) opts() WBAVerifyOptions {
	return WBAVerifyOptions{Keys: rig.resolver, Replay: rig.replay, Clock: wbaClock}
}

func (rig *wbaRig) signedRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	return r
}

// signWithAgent signs a GET for rig's key with value as the Signature-Agent
// member, used as given, so a test can sign a value the signer never emits.
func (rig *wbaRig) signWithAgent(t *testing.T, value string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
	require.NoError(t, signWebBotAuthRequest(r, rig.key, WBASignOptions{Clock: wbaClock}, value))
	return r
}

func requireWBAError(t *testing.T, err error, status int) *WBAError {
	t.Helper()
	we := AsWBAError(err)
	require.NotNil(t, we, "want *WBAError, got %v", err)
	require.Equal(t, status, we.Status, we.Reason)
	return we
}

func requireWBAStatus(t *testing.T, err error, status int) {
	t.Helper()
	_ = requireWBAError(t, err, status)
}

// signDirectoryBody serves body as origin's key directory, signed once by
// each key (WBA Appendix B.1), for directories SignKeyDirectory does not
// build.
func signDirectoryBody(t *testing.T, origin string, body []byte, keys ...any) *DirectoryResponse {
	t.Helper()
	h := http.Header{}
	h.Set("Content-Type", DirectoryMediaType)
	h.Set("Content-Digest", computeSHA256DigestHeaderForEncoding(body, BinaryEncodingRFC8941))
	var inputs, sigs sfDict
	for i, k := range keys {
		jwk, err := PublicDirectoryKey(k)
		require.NoError(t, err)
		m := sfMember{inner: true, list: []sfItem{
			{value: sfStringItem(componentAuthority), params: []sfParam{{key: "req", value: sfTrue()}}},
			{value: sfStringItem(componentContentDigst)},
		}, params: []sfParam{
			{key: "created", value: sfIntItem(wbaTestNow.Add(-time.Hour).Unix())},
			{key: "expires", value: sfIntItem(wbaTestNow.Add(24 * time.Hour).Unix())},
			{key: "keyid", value: sfStringItem(jwk.Kid)},
			{key: "tag", value: sfStringItem(DirectoryTag)},
		}}
		base, err := wbaSignatureBase(http.MethodGet, origin+DirectoryPath, h, m, true)
		require.NoError(t, err)
		sig, err := signWithKey(k, []byte(base))
		require.NoError(t, err)
		label := fmt.Sprintf("key%d", i+1)
		inputs = append(inputs, sfDictEntry{key: label, member: m})
		sigs = append(sigs, sfDictEntry{key: label, member: sfMember{item: sfItem{value: sfBare{kind: sfBinary, b: sig}}}})
	}
	if len(keys) > 0 {
		h.Set("Signature-Input", inputs.serialize())
		h.Set("Signature", sigs.serialize())
	}
	return &DirectoryResponse{Status: http.StatusOK, Header: h, Body: body}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
