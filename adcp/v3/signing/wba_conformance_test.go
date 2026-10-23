package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wbaVector is the subset of the test-vectors/wba-profile format these tests
// read (see testdata/wba-profile/README.md and the upstream README).
type wbaVector struct {
	Name         string `json:"name"`
	ReferenceNow int64  `json:"reference_now"`
	Request      struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Response *struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
	Directories map[string]struct {
		KeysRef []string `json:"keys_ref"`
	} `json:"directories"`
	TestHarnessState *struct {
		ReplayCacheEntries []struct {
			Identity   string `json:"identity"`
			Nonce      string `json:"nonce"`
			TTLSeconds int    `json:"ttl_seconds"`
		} `json:"replay_cache_entries"`
	} `json:"test_harness_state"`
	ExpectedSignatureBase map[string]string `json:"expected_signature_base"`
	ExpectedOutcome       struct {
		Success         bool              `json:"success"`
		Status          int               `json:"status"`
		Identities      map[string]string `json:"identities"`
		AcceptSignature *struct {
			Label      string   `json:"label"`
			Components string   `json:"components"`
			Parameters []string `json:"parameters"`
			Nonce      string   `json:"nonce"`
		} `json:"accept_signature"`
	} `json:"expected_outcome"`
}

type wbaVectorKey struct {
	JWK
	Identity string `json:"identity"`
}

func loadWBAKeys(t *testing.T) map[string]wbaVectorKey {
	t.Helper()
	data, err := os.ReadFile("testdata/wba-profile/keys.json")
	require.NoError(t, err)
	var doc struct {
		Keys []wbaVectorKey `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	out := map[string]wbaVectorKey{}
	for _, k := range doc.Keys {
		tp, err := JWKThumbprint(k.JWK)
		require.NoError(t, err)
		require.Equal(t, k.Kid, tp, "vector kid must be the RFC 7638 thumbprint")
		out[k.Kid] = k
	}
	return out
}

func loadWBAVectors(t *testing.T, dir string) map[string]wbaVector {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata/wba-profile", dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	out := map[string]wbaVector{}
	for _, f := range files {
		data, err := os.ReadFile(f) // #nosec G304 -- test reads vectors matched by a fixed glob under testdata.
		require.NoError(t, err)
		var v wbaVector
		require.NoError(t, json.Unmarshal(data, &v), f)
		out[filepath.Base(f)] = v
	}
	return out
}

// staticWBAKeys is a WBAKeyResolver over fixed per-origin directories.
type staticWBAKeys map[string]map[string]JWK

func (s staticWBAKeys) ResolveWBAKey(_ context.Context, origin, keyID string) (*JWK, error) {
	if k, ok := s[origin][keyID]; ok {
		pub := k.Public()
		return &pub, nil
	}
	return nil, nil
}

func vectorDirectories(t *testing.T, v wbaVector, keys map[string]wbaVectorKey) staticWBAKeys {
	t.Helper()
	dirs := staticWBAKeys{}
	for origin, d := range v.Directories {
		dirs[origin] = map[string]JWK{}
		for _, kid := range d.KeysRef {
			k, ok := keys[kid]
			require.True(t, ok, kid)
			dirs[origin][kid] = k.JWK
		}
	}
	return dirs
}

func vectorRequest(v wbaVector) *http.Request {
	r := httptest.NewRequest(v.Request.Method, v.Request.URL, strings.NewReader(v.Request.Body))
	if v.Request.Body == "" {
		r = httptest.NewRequest(v.Request.Method, v.Request.URL, nil)
	}
	for k, val := range v.Request.Headers {
		r.Header.Set(k, val)
	}
	return r
}

func verifyWBAVector(t *testing.T, v wbaVector, keys map[string]wbaVectorKey) (*VerifiedSigner, error) {
	t.Helper()
	replay := NewMemoryReplayStore(0)
	if v.TestHarnessState != nil {
		for _, e := range v.TestHarnessState.ReplayCacheEntries {
			replay.Preload(wbaReplayIdentity(e.Identity), e.Nonce, time.Duration(e.TTLSeconds)*time.Second)
		}
	}
	return VerifyWebBotAuthRequest(vectorRequest(v), WBAVerifyOptions{
		Keys:   vectorDirectories(t, v, keys),
		Replay: replay,
		Clock:  func() time.Time { return time.Unix(v.ReferenceNow, 0) },
	})
}

// TestWBAVectorsArePinned: the vendored vectors match the checksums
// recorded with their provenance in UPSTREAM.json, and every vendored file
// is pinned.
func TestWBAVectorsArePinned(t *testing.T) {
	const dir = "testdata/wba-profile"
	b, err := os.ReadFile(filepath.Join(dir, "UPSTREAM.json"))
	require.NoError(t, err)
	var up struct {
		Repository string            `json:"repository"`
		Commit     string            `json:"commit"`
		Files      map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(b, &up))
	require.Equal(t, "https://github.com/adcontextprotocol/adcp", up.Repository)
	require.Equal(t, "e59294701d733b0ea2a004a2f0c091c8eccdcaae", up.Commit)
	var vendored []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel != "UPSTREAM.json" {
			vendored = append(vendored, filepath.ToSlash(rel))
		}
		return nil
	}))
	require.Len(t, up.Files, len(vendored), "every vendored file is pinned")
	for _, name := range vendored {
		want, ok := up.Files[name]
		require.True(t, ok, name)
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- names come from a walk of testdata.
		require.NoError(t, err, name)
		sum := sha256.Sum256(data)
		require.Equal(t, want, "sha256:"+hex.EncodeToString(sum[:]), name)
	}
}

func TestWBAPositiveVectors(t *testing.T) {
	keys := loadWBAKeys(t)
	for name, v := range loadWBAVectors(t, "positive") {
		if v.Response != nil {
			continue // the directory-response vector has its own test
		}
		t.Run(name, func(t *testing.T) {
			// The signature base we compute must match the committed base
			// byte for byte, label by label.
			r := vectorRequest(v)
			inputs, err := parseSFDictionary(r.Header.Get(signatureInputHeader))
			require.NoError(t, err)
			for label, want := range v.ExpectedSignatureBase {
				m, ok := inputs.get(label)
				require.True(t, ok, label)
				got, err := wbaSignatureBase(r.Method, v.Request.URL, r.Header, m, false)
				require.NoError(t, err)
				require.Equal(t, want, got, label)
			}

			signer, err := verifyWBAVector(t, v, keys)
			require.NoError(t, err)
			require.Equal(t, ProfileTagWebBotAuth, signer.Profile)
			require.Empty(t, signer.PrincipalID, "verification alone never binds a principal")
			require.Equal(t, v.ExpectedOutcome.Identities, map[string]string{signer.Label: signer.AgentURL})
		})
	}
}

// wbaPageAcceptSignature is the Accept-Signature example on the profile
// page (Error responses), unwrapped.
const wbaPageAcceptSignature = `sig1=("@method" "@target-uri" "@authority" "content-type" "content-digest" "signature-agent";key="sig1");created;expires;nonce;keyid;alg;tag="web-bot-auth"`

func TestWBANegativeVectors(t *testing.T) {
	keys := loadWBAKeys(t)
	vectors := loadWBAVectors(t, "negative")
	require.Len(t, vectors, 6)
	for name, v := range vectors {
		t.Run(name, func(t *testing.T) {
			_, err := verifyWBAVector(t, v, keys)
			we := AsWBAError(err)
			require.NotNil(t, we, "want a *WBAError, got %v", err)
			require.Equal(t, v.ExpectedOutcome.Status, we.Status, we.Reason)
			require.False(t, we.Authorization)
			h := we.ResponseHeader()
			if v.ExpectedOutcome.Status == http.StatusBadRequest {
				require.Empty(t, h.Get(acceptSignatureHeader))
				return
			}
			accept := h.Get(acceptSignatureHeader)
			require.NotEmpty(t, accept)
			d, err := parseSFDictionary(accept)
			require.NoError(t, err)
			require.Len(t, d, 1, "one label")
			want := v.ExpectedOutcome.AcceptSignature
			m, ok := d.get(want.Label)
			require.True(t, ok, "Accept-Signature label %q in %q", want.Label, accept)
			components := sfMember{inner: true, list: m.list}.serialize()
			require.Equal(t, want.Components, components)
			var params []string
			for _, p := range m.params {
				params = append(params, p.key)
			}
			require.Equal(t, want.Parameters, params)
			tag, _ := stringParam(m.params, "tag")
			require.Equal(t, ProfileTagWebBotAuth, tag)
			nonce, fresh := stringParam(m.params, "nonce")
			if want.Nonce == "verifier-chosen" {
				require.Equal(t, http.StatusTooManyRequests, we.Status)
				require.True(t, fresh, "a replay offers a fresh nonce")
				require.True(t, validWBANonce(nonce), "the offered nonce is 64 bytes")
				return
			}
			require.Equal(t, http.StatusForbidden, we.Status)
			require.Equal(t, wbaPageAcceptSignature, accept, "the page's Accept-Signature, byte for byte")
		})
	}
}

// TestWBATwoSignaturesEachVerifyAlone: the rejection of
// 006-two-signatures is the profile's count check, not cryptography: each
// of its signatures, alone on the request, verifies against its own
// directory.
func TestWBATwoSignaturesEachVerifyAlone(t *testing.T) {
	keys := loadWBAKeys(t)
	v := loadWBAVectors(t, "negative")["006-two-signatures.json"]
	for label, origin := range map[string]string{"sig1": "https://buyer-7k3q.com", "sig2": "https://agent.brand-7k3q.com"} {
		r := vectorRequest(v)
		for _, h := range []string{"Signature-Agent", "Signature-Input", "Signature"} {
			d, err := parseSFDictionary(r.Header.Get(h))
			require.NoError(t, err)
			m, ok := d.get(label)
			require.True(t, ok)
			r.Header.Set(h, sfDict{{key: label, member: m}}.serialize())
		}
		got, err := VerifyWebBotAuthRequest(r, WBAVerifyOptions{
			Keys:   vectorDirectories(t, v, keys),
			Replay: NewMemoryReplayStore(0),
			Clock:  func() time.Time { return time.Unix(v.ReferenceNow, 0) },
		})
		require.NoError(t, err, label)
		require.Equal(t, origin, got.AgentURL)
		require.Equal(t, v.ExpectedSignatureBase[label], mustWBABase(t, r, label))
	}
}

func mustWBABase(t *testing.T, r *http.Request, label string) string {
	t.Helper()
	inputs, err := parseSFDictionary(r.Header.Get(signatureInputHeader))
	require.NoError(t, err)
	m, _ := inputs.get(label)
	base, err := wbaSignatureBase(r.Method, "https://"+r.Host+r.URL.RequestURI(), r.Header, m, false)
	require.NoError(t, err)
	return base
}

func TestWBADirectoryResponseVector(t *testing.T) {
	keys := loadWBAKeys(t)
	v := loadWBAVectors(t, "positive")["002-signed-directory-response.json"]
	require.NotNil(t, v.Response)
	resp := &DirectoryResponse{Status: v.Response.Status, Header: http.Header{}, Body: []byte(v.Response.Body)}
	for k, val := range v.Response.Headers {
		resp.Header.Set(k, val)
	}
	inputs, err := parseSFDictionary(resp.Header.Get(signatureInputHeader))
	require.NoError(t, err)
	for label, want := range v.ExpectedSignatureBase {
		m, _ := inputs.get(label)
		got, err := wbaSignatureBase(http.MethodGet, "https://buyer-7k3q.com"+DirectoryPath, resp.Header, m, true)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	got, err := VerifyDirectoryResponse("https://buyer-7k3q.com", resp, time.Unix(v.ReferenceNow, 0))
	require.NoError(t, err)
	var kids []string
	for kid := range got {
		kids = append(kids, kid)
	}
	sort.Strings(kids)
	require.Equal(t, []string{"6WsKMCObba49V3uN0vIENnk1BKAk7c9zjBJcWKyQ9_I"}, kids)
	require.Equal(t, keys[kids[0]].X, got[kids[0]].X)

	// Covering the request authority stops the key set being replayed from
	// another domain.
	_, err = VerifyDirectoryResponse("https://agent.brand-7k3q.com", resp, time.Unix(v.ReferenceNow, 0))
	require.Error(t, err)

	// The profile requires the signatures: the same body unsigned is
	// refused.
	unsigned := &DirectoryResponse{Status: resp.Status, Header: resp.Header.Clone(), Body: resp.Body}
	unsigned.Header.Del("Signature-Input")
	unsigned.Header.Del("Signature")
	_, err = VerifyDirectoryResponse("https://buyer-7k3q.com", unsigned, time.Unix(v.ReferenceNow, 0))
	require.Error(t, err)
}

// TestWBASignerReproducesVector signs the committed request with the
// published seed. Ed25519 is deterministic, so the Signature must match
// byte for byte.
func TestWBASignerReproducesVector(t *testing.T) {
	keys := loadWBAKeys(t)
	v := loadWBAVectors(t, "positive")["001-single-signer-request.json"]
	seed, err := b64UrlDecode(keys["6WsKMCObba49V3uN0vIENnk1BKAk7c9zjBJcWKyQ9_I"].TestOnlyPrivateD)
	require.NoError(t, err)
	priv := ed25519.NewKeyFromSeed(seed)

	r := httptest.NewRequest(v.Request.Method, v.Request.URL, strings.NewReader(v.Request.Body))
	r.Header.Set("Content-Type", "application/json")
	inputs, err := parseSFDictionary(v.Request.Headers["Signature-Input"])
	require.NoError(t, err)
	m, _ := inputs.get("sig1")
	created, _ := intParam(m.params, "created")
	nonce, _ := stringParam(m.params, "nonce")
	require.NoError(t, SignWebBotAuthRequest(r, priv, WBASignOptions{
		AgentOrigin:     "https://buyer-7k3q.com",
		CreatedOverride: created,
		NonceOverride:   nonce,
	}))
	require.Equal(t, v.Request.Headers["Signature-Agent"], r.Header.Get("Signature-Agent"))
	require.Equal(t, v.Request.Headers["Content-Digest"], r.Header.Get("Content-Digest"))
	require.Equal(t, v.Request.Headers["Signature-Input"], r.Header.Get("Signature-Input"))
	require.Equal(t, v.Request.Headers["Signature"], r.Header.Get("Signature"))
}
