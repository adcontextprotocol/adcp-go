package signing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Vector is the JSON shape of each AdCP conformance vector, used for both
// the request-signing and webhook-signing suites.
type Vector struct {
	Name          string          `json:"name"`
	SpecReference string          `json:"spec_reference"`
	ReferenceNow  int64           `json:"reference_now"`
	Request       VectorRequest   `json:"request"`
	Capability    VectorCap       `json:"verifier_capability"`
	JWKSRef       []string        `json:"jwks_ref"`
	JWKSOverride  *JWKS           `json:"jwks_override"`
	HarnessState  *VectorState    `json:"test_harness_state"`
	ExpectedBase  string          `json:"expected_signature_base"`
	Expected      VectorOutcome   `json:"expected_outcome"`
	Comment       json.RawMessage `json:"$comment"`
}

// UnmarshalJSON accepts both shapes that the two upstream vector suites use
// for jwks_override: the JWKS shape `{"keys":[...]}` (request-signing) and
// the map-of-kid-to-jwk shape `{"<kid>":{...}}` (webhook-signing).
func (v *Vector) UnmarshalJSON(data []byte) error {
	type alias Vector
	aux := &struct {
		JWKSOverride json.RawMessage `json:"jwks_override"`
		*alias
	}{alias: (*alias)(v)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.JWKSOverride) == 0 || string(aux.JWKSOverride) == "null" {
		return nil
	}
	var asJWKS JWKS
	if err := json.Unmarshal(aux.JWKSOverride, &asJWKS); err == nil && len(asJWKS.Keys) > 0 {
		v.JWKSOverride = &asJWKS
		return nil
	}
	var asMap map[string]JWK
	if err := json.Unmarshal(aux.JWKSOverride, &asMap); err != nil {
		return err
	}
	jwks := &JWKS{}
	for _, k := range asMap {
		jwks.Keys = append(jwks.Keys, k)
	}
	v.JWKSOverride = jwks
	return nil
}

type VectorRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type VectorCap struct {
	Supported           bool     `json:"supported"`
	CoversContentDigest string   `json:"covers_content_digest"`
	RequiredFor         []string `json:"required_for"`
}

type VectorState struct {
	// Request-signing shape:
	ReplayCacheEntries []replayEntry           `json:"replay_cache_entries"`
	ReplayCacheCapHit  *capHit                 `json:"replay_cache_per_keyid_cap_hit"`
	RevocationList     *revocationListSnapshot `json:"revocation_list"`
	// Webhook-signing shape (adcontextprotocol/adcp#2445 uses a flatter vocabulary):
	RevokedKids                []string `json:"revoked_kids"`
	PerKeyIDCapFilledFor       string   `json:"per_keyid_cap_filled_for"`
	RevocationListStaleSeconds int64    `json:"revocation_list_stale_seconds"`
}

type replayEntry struct {
	KeyID      string `json:"keyid"`
	Nonce      string `json:"nonce"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type capHit struct {
	KeyID string `json:"keyid"`
}

type revocationListSnapshot struct {
	Issuer      string   `json:"issuer"`
	Updated     string   `json:"updated"`
	NextUpdate  string   `json:"next_update"`
	RevokedKids []string `json:"revoked_kids"`
	RevokedJtis []string `json:"revoked_jtis"`
}

type VectorOutcome struct {
	Success       bool   `json:"success"`
	ErrorCode     string `json:"error_code"`
	FailedStep    any    `json:"failed_step"`
	VerifiedLabel string `json:"verified_label"`
}

// operationFromURL derives the AdCP operation name from the URL path: the last
// segment (e.g. /adcp/create_media_buy → create_media_buy).
func operationFromURL(rawURL string) string {
	// Use the last non-empty path segment.
	_, after, ok := strings.Cut(rawURL, "://")
	p := rawURL
	if ok {
		p = after
	}
	if slash := strings.IndexByte(p, '/'); slash >= 0 {
		p = p[slash:]
	}
	if q := strings.IndexByte(p, '?'); q >= 0 {
		p = p[:q]
	}
	_, op := path.Split(strings.TrimRight(p, "/"))
	return op
}

func loadVectorFile(t *testing.T, rel string) Vector {
	t.Helper()
	data, err := os.ReadFile(rel)
	require.NoError(t, err, "open %s", rel)
	var v Vector
	require.NoError(t, json.Unmarshal(data, &v), "parse %s", rel)
	return v
}

// buildResolverForVector builds a StaticJWKSResolver from the vector's jwks_ref
// (entries selected from testdata/request-signing/keys.json) or jwks_override.
// Agent URL is a deterministic stub.
func buildResolverForVector(t *testing.T, v Vector) *StaticJWKSResolver {
	t.Helper()
	resolver := NewStaticJWKSResolver()
	var jwks *JWKS
	if v.JWKSOverride != nil {
		jwks = v.JWKSOverride
	} else {
		all := loadSpecKeys(t)
		jwks = &JWKS{}
		for _, kid := range v.JWKSRef {
			k := all.Find(kid)
			if k != nil {
				jwks.Keys = append(jwks.Keys, *k)
			}
		}
	}
	for i := range jwks.Keys {
		k := &jwks.Keys[i]
		resolver.Put(k.Kid, k, "https://signer.example.com/agent")
	}
	return resolver
}

// buildRequestForVector converts the vector's request spec into an http.Request.
func buildRequestForVector(t *testing.T, v Vector) *http.Request {
	t.Helper()
	var body io.Reader
	if v.Request.Body != "" {
		body = bytes.NewReader([]byte(v.Request.Body))
	}
	req, err := http.NewRequest(v.Request.Method, v.Request.URL, body)
	require.NoError(t, err)
	for name, val := range v.Request.Headers {
		req.Header.Set(name, val)
	}
	return req
}

// runVector runs a single vector and returns the verify result.
func runVector(t *testing.T, v Vector) (*VerifiedSigner, error) {
	t.Helper()
	req := buildRequestForVector(t, v)
	resolver := buildResolverForVector(t, v)

	clock := func() time.Time { return time.Unix(v.ReferenceNow, 0).UTC() }
	replay := NewMemoryReplayStore(0)
	replay.withClock(clock)
	revocation := NewStaticRevocationList(nil)

	if v.HarnessState != nil {
		for _, e := range v.HarnessState.ReplayCacheEntries {
			// ReplayStore keys on decoded nonce bytes; the vector supplies the
			// on-wire base64url string.
			nonceBytes, err := b64UrlDecode(e.Nonce)
			require.NoError(t, err)
			replay.Preload(e.KeyID, string(nonceBytes), time.Duration(e.TTLSeconds)*time.Second)
		}
		if v.HarnessState.ReplayCacheCapHit != nil {
			replay.MarkKeyIDAtCap(v.HarnessState.ReplayCacheCapHit.KeyID)
		}
		if v.HarnessState.RevocationList != nil {
			revocation.SetRevoked(v.HarnessState.RevocationList.RevokedKids)
		}
	}

	return VerifyRequestSignature(req, VerifyOptions{
		OperationName:       operationFromURL(v.Request.URL),
		RequiredFor:         v.Capability.RequiredFor,
		ContentDigestPolicy: DigestPolicy(v.Capability.CoversContentDigest),
		Scheme:              "https",
		Resolver:            resolver,
		Revocation:          revocation,
		Replay:              replay,
		Clock:               clock,
	})
}

// TestExpectedSignatureBaseBytes checks our canonicalization against every
// positive vector's expected_signature_base. A byte mismatch here flags a
// canonicalization bug BEFORE the crypto verify step.
func TestExpectedSignatureBaseBytes(t *testing.T) {
	dir := "testdata/request-signing/positive"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			v := loadVectorFile(t, filepath.Join(dir, name))
			if v.ExpectedBase == "" || name == "004-multiple-signature-labels.json" {
				// 004 has no expected_signature_base (it's a relay-model test).
				return
			}
			parsed, err := parseSignatureInput(v.Request.Headers["Signature-Input"])
			require.NoError(t, err)

			canonicalURI, err := canonicalTargetURI(v.Request.URL)
			require.NoError(t, err)
			req := buildRequestForVector(t, v)
			scheme := "https"
			if strings.HasPrefix(v.Request.URL, "http://") {
				scheme = "http"
			}
			authority := canonicalAuthority(authorityOf(req), scheme)
			values := map[string]string{
				componentMethod:      strings.ToUpper(v.Request.Method),
				componentTargetURI:   canonicalURI,
				componentAuthority:   authority,
				componentContentType: v.Request.Headers["Content-Type"],
			}
			if slicesContains(parsed.components, componentContentDigst) {
				values[componentContentDigst] = v.Request.Headers["Content-Digest"]
			}
			base, err := buildSignatureBase(parsed.components, values, parsed.paramsText)
			require.NoError(t, err)
			assert.Equal(t, v.ExpectedBase, base, "canonical base mismatch for %s", name)
		})
	}
}

func slicesContains(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

// TestPositiveVectors runs each positive vector through the full verifier.
func TestPositiveVectors(t *testing.T) {
	dir := "testdata/request-signing/positive"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			v := loadVectorFile(t, filepath.Join(dir, name))
			signer, err := runVector(t, v)
			if !assert.NoError(t, err, "positive vector %s must verify", name) {
				if e := AsError(err); e != nil {
					t.Logf("rejected with code=%s detail=%s", e.Code, e.Detail)
				}
				return
			}
			assert.NotNil(t, signer)
		})
	}
}

// TestNegativeVectors runs each negative vector through the full verifier
// and asserts byte-for-byte match on expected_outcome.error_code.
func TestNegativeVectors(t *testing.T) {
	dir := "testdata/request-signing/negative"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			v := loadVectorFile(t, filepath.Join(dir, name))
			_, err := runVector(t, v)
			require.Error(t, err, "negative vector %s must reject", name)
			e := AsError(err)
			require.NotNil(t, e, "vector %s: want *Error, got %v", name, err)
			assert.Equal(t, v.Expected.ErrorCode, string(e.Code), "vector %s wrong error code", name)
		})
	}
}
