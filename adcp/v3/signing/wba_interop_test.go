package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Web Bot Auth interoperability fixtures from the TypeScript SDK, generated
// there against Cloudflare's reference packages (web-bot-auth@0.2.0,
// http-message-sig@0.3.0). Provenance and checksums: testdata/wba-interop/
// UPSTREAM.json.
const wbaInteropDir = "testdata/wba-interop"

type wbaInteropFixture struct {
	Signer  string `json:"signer"`
	Request struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"request"`
	Directory struct {
		URL     string            `json:"url"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"directory"`
	ReferenceNow int64 `json:"reference_now"`
	Expected     struct {
		Verifies      bool   `json:"verifies"`
		Identity      string `json:"identity"`
		KeyID         string `json:"keyid"`
		SignatureBase string `json:"signature_base"`
	} `json:"expected"`
}

var wbaInteropFixtures = []string{
	"cloudflare-signed-body-request.json",
	"cloudflare-signed-percent-encoded-path.json",
	"adcp-client-signed-body-request.json",
	"adcp-client-signed-percent-encoded-path.json",
}

func loadWBAInteropFixture(t *testing.T, name string) wbaInteropFixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wbaInteropDir, name)) // #nosec G304 -- test reads a fixed fixture name under testdata.
	require.NoError(t, err)
	var f wbaInteropFixture
	require.NoError(t, json.Unmarshal(b, &f))
	return f
}

// TestWBAInteropFixturesArePinned: the vendored files match the checksums
// recorded with their provenance.
func TestWBAInteropFixturesArePinned(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(wbaInteropDir, "UPSTREAM.json"))
	require.NoError(t, err)
	var up struct {
		Repository string            `json:"repository"`
		Commit     string            `json:"commit"`
		Path       string            `json:"path"`
		Files      map[string]string `json:"files"`
	}
	require.NoError(t, json.Unmarshal(b, &up))
	require.Equal(t, "https://github.com/adcontextprotocol/adcp-client", up.Repository)
	require.Len(t, up.Commit, 40)
	entries, err := os.ReadDir(wbaInteropDir)
	require.NoError(t, err)
	require.Len(t, up.Files, len(entries)-1, "every vendored file is pinned")
	for name, want := range up.Files {
		data, err := os.ReadFile(filepath.Join(wbaInteropDir, name)) // #nosec G304 -- names come from the pinned UPSTREAM.json under testdata.
		require.NoError(t, err, name)
		sum := sha256.Sum256(data)
		require.Equal(t, want, "sha256:"+hex.EncodeToString(sum[:]), name)
	}
}

// interopDirectoryFetcher serves the fixture's key directory at its URL
// only, so the resolver validates it exactly as fetched (including its
// Appendix B signature).
func interopDirectoryFetcher(t *testing.T, f wbaInteropFixture) DirectoryFetcher {
	return func(_ context.Context, url string) (*DirectoryResponse, error) {
		if url != f.Directory.URL {
			return &DirectoryResponse{Status: http.StatusNotFound, Header: http.Header{}}, nil
		}
		h := http.Header{}
		for k, v := range f.Directory.Headers {
			h.Set(k, v)
		}
		return &DirectoryResponse{Status: f.Directory.Status, Header: h, Body: []byte(f.Directory.Body)}, nil
	}
}

// receivedInteropRequest builds the request as a server receives it: the
// origin-form request-target exactly as sent (percent-encoding kept) and
// the Host field.
func receivedInteropRequest(t *testing.T, f wbaInteropFixture, headers map[string]string) *http.Request {
	t.Helper()
	const prefix = "https://seller.example.com"
	require.True(t, strings.HasPrefix(f.Request.URL, prefix+"/"), f.Request.URL)
	r := httptest.NewRequest(f.Request.Method, f.Request.URL, strings.NewReader(f.Request.Body))
	r.RequestURI = strings.TrimPrefix(f.Request.URL, prefix)
	r.Host = "seller.example.com"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func interopVerifyOptions(t *testing.T, f wbaInteropFixture) WBAVerifyOptions {
	clock := func() time.Time { return time.Unix(f.ReferenceNow, 0) }
	return WBAVerifyOptions{
		// Both directories carry Appendix B signatures, which the profile
		// requires.
		Keys:        NewDirectoryResolver(DirectoryResolverOptions{Fetch: interopDirectoryFetcher(t, f), Clock: clock}),
		Replay:      NewMemoryReplayStore(0),
		Clock:       clock,
		Scheme:      "https",
		Authorities: []string{"seller.example.com"},
	}
}

// TestWBAInteropFixturesVerify: the Go verifier accepts requests signed by
// Cloudflare's web-bot-auth and by the TypeScript SDK, percent-encoded
// target paths included, against directories signed by each, and builds the
// same RFC 9421 signature base.
func TestWBAInteropFixturesVerify(t *testing.T) {
	for _, name := range wbaInteropFixtures {
		t.Run(name, func(t *testing.T) {
			f := loadWBAInteropFixture(t, name)
			require.True(t, f.Expected.Verifies)
			r := receivedInteropRequest(t, f, f.Request.Headers)

			inputs, err := parseSFDictionary(r.Header.Get(signatureInputHeader))
			require.NoError(t, err)
			member, ok := inputs.get("sig1")
			require.True(t, ok)
			target, ok := receivedTargetURI(r, "https")
			require.True(t, ok)
			require.Equal(t, f.Request.URL, target, "@target-uri is the URI as received")
			base, err := wbaSignatureBase(r.Method, target, r.Header, member, false)
			require.NoError(t, err)
			require.Equal(t, f.Expected.SignatureBase, base)

			signer, err := VerifyWebBotAuthRequest(r, interopVerifyOptions(t, f))
			require.NoError(t, err)
			require.Equal(t, f.Expected.Identity, signer.AgentURL)
			require.Equal(t, f.Expected.KeyID, signer.KeyID)
			require.Equal(t, AlgEd25519, signer.Algorithm)

			// The same request with its path percent-decoded is a
			// different target URI and must not verify.
			if strings.Contains(f.Request.URL, "%7E") {
				decoded := receivedInteropRequest(t, f, f.Request.Headers)
				decoded.RequestURI = strings.ReplaceAll(decoded.RequestURI, "%7E", "~")
				_, err := VerifyWebBotAuthRequest(decoded, interopVerifyOptions(t, f))
				requireWBAStatus(t, err, http.StatusForbidden)
			}
		})
	}
}

// interopSigningKey returns the fixtures' test-only Ed25519 key.
func interopSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wbaInteropDir, "keys.json"))
	require.NoError(t, err)
	var doc struct {
		Keys []struct {
			Kty     string `json:"kty"`
			Crv     string `json:"crv"`
			X       string `json:"x"`
			Private struct {
				D string `json:"d"`
			} `json:"_private_jwk_for_test_only"`
		} `json:"keys"`
	}
	require.NoError(t, json.Unmarshal(b, &doc))
	for _, k := range doc.Keys {
		if k.Kty == "OKP" && k.Crv == "Ed25519" {
			seed, err := base64.RawURLEncoding.DecodeString(k.Private.D)
			require.NoError(t, err)
			key := ed25519.NewKeyFromSeed(seed)
			require.Equal(t, k.X, base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))
			return key
		}
	}
	t.Fatal("no Ed25519 key in keys.json")
	return nil
}

// TestWBAInteropGoSignerMatches: with the fixture's created time and nonce,
// the Go signer reproduces the TypeScript SDK's signed requests byte for
// byte (Ed25519 is deterministic and both use the profile's component
// order), and its output verifies under the Cloudflare-signed directory
// too. A Cloudflare-style padded-base64 nonce signs and verifies as well.
func TestWBAInteropGoSignerMatches(t *testing.T) {
	key := interopSigningKey(t)
	for _, pair := range [][2]string{
		{"adcp-client-signed-body-request.json", "cloudflare-signed-body-request.json"},
		{"adcp-client-signed-percent-encoded-path.json", "cloudflare-signed-percent-encoded-path.json"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			ts, cf := loadWBAInteropFixture(t, pair[0]), loadWBAInteropFixture(t, pair[1])
			in, err := parseSFDictionary(ts.Request.Headers["signature-input"])
			require.NoError(t, err)
			m, _ := in.get("sig1")
			nonce, _ := stringParam(m.params, "nonce")
			created, _ := intParam(m.params, "created")

			sign := func(nonce string) map[string]string {
				r := httptest.NewRequest(ts.Request.Method, ts.Request.URL, strings.NewReader(ts.Request.Body))
				r.Header.Set("Content-Type", ts.Request.Headers["content-type"])
				require.NoError(t, SignWebBotAuthRequest(r, key, WBASignOptions{
					AgentOrigin: ts.Expected.Identity, CreatedOverride: created, NonceOverride: nonce,
					Clock: func() time.Time { return time.Unix(ts.ReferenceNow, 0) },
				}))
				out := map[string]string{}
				for _, h := range []string{"content-type", "content-digest", "signature-agent", "signature-input", "signature"} {
					out[h] = r.Header.Get(h)
				}
				return out
			}
			goHeaders := sign(nonce)
			require.Equal(t, ts.Request.Headers, goHeaders, "byte-for-byte with the TypeScript SDK")

			for _, f := range []wbaInteropFixture{ts, cf} {
				_, err := VerifyWebBotAuthRequest(receivedInteropRequest(t, f, goHeaders), interopVerifyOptions(t, f))
				require.NoError(t, err, "Go-signed request under the %s directory", f.Signer)
			}

			// Cloudflare's signer emits 64 random bytes in padded standard
			// base64, whose alphabet includes "+" and "/"; the profile asks
			// for base64url. Both encodings of 64 bytes are accepted.
			for _, nonce := range []string{strings.Repeat("CQkJ", 21) + "CQ==", strings.Repeat("+/+/", 21) + "+w=="} {
				require.True(t, validWBANonce(nonce))
				_, err = VerifyWebBotAuthRequest(receivedInteropRequest(t, cf, sign(nonce)), interopVerifyOptions(t, cf))
				require.NoError(t, err, nonce)
			}
		})
	}
}
