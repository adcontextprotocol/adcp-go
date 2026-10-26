package signing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/stretchr/testify/require"
)

func TestCanonicalOrigin(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		// The url-canonicalization.mdx example.
		{"HTTPS://User@Seller.Example.COM.:443/mcp?x=1", "https://seller.example.com", true},
		{"https://seller.example.com:8443/a/b", "https://seller.example.com:8443", true},
		{"http://seller.example.com:80/", "http://seller.example.com", true},
		{"https://[2001:db8::1]:443/x", "https://[2001:db8::1]", true},
		{"https://seller.example.com", "https://seller.example.com", true},
		{"https://seller..example.com/x", "", false},
		{"https://seller.example.com../x", "", false},
		{"ftp://seller.example.com/x", "", false},
		{"/relative", "", false},
		{"https://bücher.example/x", "", false},
	}
	for _, tc := range cases {
		got, err := CanonicalOrigin(tc.in)
		if tc.ok {
			require.NoError(t, err, tc.in)
			require.Equal(t, tc.want, got, tc.in)
		} else {
			require.Error(t, err, tc.in)
		}
	}
}

func TestWebBotAuthOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://Buyer.Example.com/agent/mcp": "https://buyer.example.com",
		"https://buyer.example.com.":          "https://buyer.example.com",
	} {
		got, err := webBotAuthOrigin(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got)
	}
	for _, in := range []string{
		"http://buyer.example.com", "https://user@buyer.example.com", "https://buyer.example.com/a b",
		"https://127.0.0.1", "https://10.0.0.8", "https://localhost", "https://[::1]", "not a url", "",
	} {
		_, err := webBotAuthOrigin(in)
		require.Error(t, err, in)
	}
}

func TestJWKThumbprint(t *testing.T) {
	// RFC 8037 Appendix A.3.
	tp, err := JWKThumbprint(JWK{Kty: "OKP", Crv: "Ed25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"})
	require.NoError(t, err)
	require.Equal(t, "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k", tp)
	// Members outside the required set do not change the thumbprint.
	tp2, err := JWKThumbprint(JWK{Kty: "OKP", Crv: "Ed25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", Kid: "x", Use: "sig", Alg: "EdDSA"})
	require.NoError(t, err)
	require.Equal(t, tp, tp2)
	for _, bad := range []JWK{{Kty: "OKP"}, {Kty: "EC", Crv: "P-256", X: "a"}, {Kty: "RSA"}} {
		_, err := JWKThumbprint(bad)
		require.Error(t, err)
	}
	jwk, err := PublicDirectoryKey(newEd25519(t))
	require.NoError(t, err)
	require.Len(t, jwk.Kid, wbaThumbprintLen)
	require.Equal(t, "sig", jwk.Use)
	require.Empty(t, jwk.AdcpUse, "purpose belongs to the origin, not the key")
	require.Empty(t, jwk.PrivateD)
	// ES256 is not a Web Bot Auth algorithm.
	_, err = PublicDirectoryKey(newP256(t))
	require.Error(t, err)
}

func TestUsesWebBotAuth(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		want   bool
	}{
		{"unsigned", nil, false},
		{"rfc 9421", map[string]string{"Signature-Input": `sig1=("@method");created=1;tag="adcp/request-signing/v1"`}, false},
		{"signature-agent present", map[string]string{"Signature-Agent": `sig1="https://b.example.com"`}, true},
		{"uncovered signature-agent on rfc 9421", map[string]string{"Signature-Agent": `x`, "Signature-Input": `sig1=("@method");tag="adcp/request-signing/v1"`}, true},
		{"wba tag", map[string]string{"Signature-Input": `sig1=("@method");tag="web-bot-auth"`}, true},
		{"wba tag on a second label", map[string]string{"Signature-Input": `a=("@method");tag="adcp/request-signing/v1", b=("@method");tag="web-bot-auth"`}, true},
		{"malformed input naming the tag", map[string]string{"Signature-Input": `sig1=("@method";tag="web-bot-auth"`}, true},
	}
	for _, tc := range cases {
		h := http.Header{}
		for k, v := range tc.header {
			h.Set(k, v)
		}
		require.Equal(t, tc.want, UsesWebBotAuth(h), tc.name)
	}
}

func TestWBASignVerifyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  any
		body string
	}{
		{"ed25519 with body", newEd25519(t), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_products"}}`},
		{"ed25519 read without body", newEd25519(t), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs := newFakeDirectories()
			origin := "https://agent.buyer.example.com"
			other := newEd25519(t)
			dirs.publish(t, origin, wbaTestNow.Add(-time.Hour), 24*time.Hour, other, tc.key)
			resolver := NewDirectoryResolver(DirectoryResolverOptions{Fetch: dirs.fetch, Clock: wbaClock})
			var r *http.Request
			if tc.body == "" {
				r = httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_products?x=1", nil)
			} else {
				r = httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(tc.body))
				r.Header.Set("Content-Type", "application/json")
			}
			require.NoError(t, SignWebBotAuthRequest(r, tc.key, WBASignOptions{AgentOrigin: origin + "/mcp", Clock: wbaClock}))
			require.Equal(t, `sig1="https://agent.buyer.example.com"`, r.Header.Get("Signature-Agent"))
			if tc.body == "" {
				require.Empty(t, r.Header.Get("Content-Digest"))
			}
			require.Contains(t, r.Header.Get("Signature-Input"), `;alg="ed25519";`)
			signer, err := VerifyWebBotAuthRequest(r, WBAVerifyOptions{Keys: resolver, Replay: NewMemoryReplayStore(0), Clock: wbaClock})
			require.NoError(t, err)
			jwk, _ := PublicDirectoryKey(tc.key)
			require.Equal(t, origin, signer.AgentURL)
			require.Equal(t, jwk.Kid, signer.KeyID)
			require.Equal(t, ProfileTagWebBotAuth, signer.Profile)
			if tc.body != "" {
				// The body is still readable downstream.
				b := make([]byte, len(tc.body))
				_, _ = r.Body.Read(b)
				require.Equal(t, tc.body, string(b))
			}
		})
	}
	// A P-256 key signs only under the AdCP RFC 9421 profile.
	r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_products", nil)
	require.Error(t, SignWebBotAuthRequest(r, newP256(t), WBASignOptions{AgentOrigin: "https://agent.buyer.example.com"}))
	_, err := NewSigner(SignerOptions{PrivateKey: newP256(t), AgentOrigin: "https://agent.buyer.example.com"})
	require.Error(t, err)
}

func TestWBARejections(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_media_buy"}}`
	type tamper func(t *testing.T, rig *wbaRig, r *http.Request)
	resign := func(t *testing.T, rig *wbaRig, r *http.Request, opts WBASignOptions) {
		for _, h := range []string{"Signature", "Signature-Input", "Signature-Agent"} {
			r.Header.Del(h)
		}
		opts.AgentOrigin = rig.origin
		if opts.Clock == nil {
			opts.Clock = wbaClock
		}
		require.NoError(t, SignWebBotAuthRequest(r, rig.key, opts))
	}
	cases := []struct {
		name   string
		body   string
		tamper tamper
		status int
		// discovery marks a key discovery failure (WBA §6.10): 403 with
		// Accept-Signature, distinguishable so the middleware can apply
		// its local policy.
		discovery bool
	}{
		{"body changed after signing", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Body = http.NoBody
			r.Body = newBody(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"other"}}`)
		}, http.StatusForbidden, false},
		{"signature-agent swapped to an origin with no directory", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1="https://attacker.example.com"`)
		}, http.StatusForbidden, true},
		{"signature-agent swapped to another origin's directory", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.publish(t, "https://attacker.example.com", wbaTestNow.Add(-time.Hour), 24*time.Hour, rig.key)
			r.Header.Set("Signature-Agent", `sig1="https://attacker.example.com"`)
		}, http.StatusForbidden, false},
		{"signature-agent type other than directory", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1="https://buyer.example.com";type=jwks`)
		}, http.StatusForbidden, false},
		{"signature-agent member missing", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `other="https://buyer.example.com"`)
		}, http.StatusForbidden, false},
		{"signature-agent not an sf-string", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1=buyer`)
		}, http.StatusForbidden, false},
		{"signature-agent over http", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1="http://buyer.example.com"`)
		}, http.StatusForbidden, false},
		{"expired", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			resign(t, rig, r, WBASignOptions{CreatedOverride: wbaTestNow.Add(-10 * time.Minute).Unix()})
		}, http.StatusForbidden, false},
		{"created in the future beyond skew", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			resign(t, rig, r, WBASignOptions{CreatedOverride: wbaTestNow.Add(2 * time.Minute).Unix()})
		}, http.StatusForbidden, false},
		{"short nonce", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `nonce="`, `nonce="x`, 1))
			in, _ := parseSFDictionary(r.Header.Get("Signature-Input"))
			m, _ := in.get("sig1")
			for i := range m.params {
				if m.params[i].key == "nonce" {
					m.params[i].value = sfStringItem("short")
				}
			}
			r.Header.Set("Signature-Input", sfDict{{key: "sig1", member: m}}.serialize())
		}, http.StatusForbidden, false},
		{"alg not a Web Bot Auth algorithm", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `alg="ed25519"`, `alg="ecdsa-p256-sha256"`, 1))
		}, http.StatusForbidden, false},
		{"alg rsa-pss-sha512 (the profile is Ed25519 only)", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `alg="ed25519"`, `alg="rsa-pss-sha512"`, 1))
		}, http.StatusForbidden, false},
		{"alg missing", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `;alg="ed25519"`, ``, 1))
		}, http.StatusForbidden, false},
		{"alg not an sf-string", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `alg="ed25519"`, `alg=ed25519`, 1))
		}, http.StatusForbidden, false},
		{"two web-bot-auth signatures", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			other := newEd25519(t)
			rig.dirs.publish(t, "https://agency.example.com", wbaTestNow.Add(-time.Hour), 24*time.Hour, other)
			two := twoSignatureRequest(t, rig, other, "https://agency.example.com", body)
			for _, h := range []string{"Signature", "Signature-Input", "Signature-Agent"} {
				r.Header.Set(h, two.Header.Get(h))
			}
		}, http.StatusForbidden, false},
		{"a second Signature-Agent member", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", r.Header.Get("Signature-Agent")+`, sig2="https://other.example.com"`)
		}, http.StatusForbidden, false},
		{"keyid not a thumbprint", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			in, _ := parseSFDictionary(r.Header.Get("Signature-Input"))
			m, _ := in.get("sig1")
			for i := range m.params {
				if m.params[i].key == "keyid" {
					m.params[i].value = sfStringItem("buyer-key-1")
				}
			}
			r.Header.Set("Signature-Input", sfDict{{key: "sig1", member: m}}.serialize())
		}, http.StatusForbidden, false},
		{"key from another origin's directory", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			other := newEd25519(t)
			rig.dirs.publish(t, "https://other.example.com", wbaTestNow.Add(-time.Hour), 24*time.Hour, other)
			for _, h := range []string{"Signature", "Signature-Input", "Signature-Agent"} {
				r.Header.Del(h)
			}
			require.NoError(t, SignWebBotAuthRequest(r, other, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
		}, http.StatusForbidden, false},
		{"content-type not covered on a body", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `"content-type" `, ``, 1))
		}, http.StatusForbidden, false},
		{"signature-agent not covered", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), ` "signature-agent";key="sig1"`, ``, 1))
		}, http.StatusForbidden, false},
		{"repeated covered component", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `("@method"`, `("@method" "@method"`, 1))
		}, http.StatusForbidden, false},
		{"unsupported derived component", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", strings.Replace(r.Header.Get("Signature-Input"), `("@method"`, `("@path" "@method"`, 1))
		}, http.StatusForbidden, false},
		{"labels differ between Signature and Signature-Input", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature", strings.Replace(r.Header.Get("Signature"), "sig1=", "sig2=", 1))
		}, http.StatusBadRequest, false},
		{"duplicate Signature-Input member", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+", "+r.Header.Get("Signature-Input"))
		}, http.StatusBadRequest, false},
		{"duplicate parameter", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`;tag="web-bot-auth"`)
		}, http.StatusBadRequest, false},
		{"duplicate created parameter", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`;created=1`)
		}, http.StatusBadRequest, false},
		{"duplicate Signature member", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature", r.Header.Get("Signature")+", "+r.Header.Get("Signature"))
		}, http.StatusBadRequest, false},
		{"duplicate Signature-Agent member", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", r.Header.Get("Signature-Agent")+", "+r.Header.Get("Signature-Agent"))
		}, http.StatusBadRequest, false},
		{"duplicate Signature-Agent member with another origin last", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", r.Header.Get("Signature-Agent")+`, sig1="https://other.example.com"`)
		}, http.StatusBadRequest, false},
		{"duplicate Signature-Agent parameter", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", r.Header.Get("Signature-Agent")+`;x=1;x=2`)
		}, http.StatusBadRequest, false},
		{"Signature-Input on two lines", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Add("Signature-Input", `sig2=("@method");tag="web-bot-auth"`)
		}, http.StatusBadRequest, false},
		{"Signature-Agent on two lines", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Add("Signature-Agent", `sig2="https://b.example.com"`)
		}, http.StatusBadRequest, false},
		{"Signature-Agent missing with a wba tag", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Del("Signature-Agent")
		}, http.StatusForbidden, false},
		{"Signature-Agent that does not parse", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Agent", `sig1="unterminated`)
		}, http.StatusBadRequest, false},
		{"signed body with case-folded duplicate keys", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","Name":"b"}}`, nil, http.StatusBadRequest, false},
		{"Signature not sf-binary", body, func(t *testing.T, _ *wbaRig, r *http.Request) {
			r.Header.Set("Signature", `sig1="abc"`)
		}, http.StatusBadRequest, false},
		{"malformed signed JSON body", "{not json", nil, http.StatusBadRequest, false},
		{"duplicate keys in signed JSON body", `{"a":1,"a":2}`, nil, http.StatusBadRequest, false},
		{"directory cannot be fetched: discovery failure, not an outage", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.fail(rig.origin, errors.New("timeout"))
		}, http.StatusForbidden, true},
		{"directory 5xx: discovery failure", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin] = &DirectoryResponse{Status: http.StatusBadGateway, Header: http.Header{}}
			rig.dirs.mu.Unlock()
		}, http.StatusForbidden, true},
		{"directory 429: discovery failure", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin] = &DirectoryResponse{Status: http.StatusTooManyRequests, Header: http.Header{}}
			rig.dirs.mu.Unlock()
		}, http.StatusForbidden, true},
		{"directory absent (404): discovery failure", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.mu.Lock()
			delete(rig.dirs.dirs, rig.origin)
			rig.dirs.mu.Unlock()
		}, http.StatusForbidden, true},
		{"directory absent (410): discovery failure", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin] = &DirectoryResponse{Status: http.StatusGone, Header: http.Header{}}
			rig.dirs.mu.Unlock()
		}, http.StatusForbidden, true},
		{"directory that does not parse: discovery failure", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin] = &DirectoryResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {DirectoryMediaType}}, Body: []byte(`{"keys":`)}
			rig.dirs.mu.Unlock()
		}, http.StatusForbidden, true},
		{"nonce of 32 bytes", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", replaceNonce(r.Header.Get("Signature-Input"), base64.RawURLEncoding.EncodeToString(make([]byte, 32))))
		}, http.StatusForbidden, false},
		{"nonce of 65 bytes", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", replaceNonce(r.Header.Get("Signature-Input"), base64.RawURLEncoding.EncodeToString(make([]byte, 65))))
		}, http.StatusForbidden, false},
		{"nonce in unpadded standard base64", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			r.Header.Set("Signature-Input", replaceNonce(r.Header.Get("Signature-Input"), base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 64))))
		}, http.StatusForbidden, false},
		{"nonce in non-canonical base64", body, func(t *testing.T, rig *wbaRig, r *http.Request) {
			n := base64.RawURLEncoding.EncodeToString(make([]byte, 64))
			r.Header.Set("Signature-Input", replaceNonce(r.Header.Get("Signature-Input"), n[:len(n)-1]+"B"))
		}, http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWBARig(t)
			r := rig.signedRequest(t, tc.body)
			if tc.tamper != nil {
				tc.tamper(t, rig, r)
			}
			_, err := VerifyWebBotAuthRequest(r, rig.opts())
			we := requireWBAError(t, err, tc.status)
			require.False(t, we.Authorization, "the signature path never answers with an authorization error")
			require.Equal(t, tc.discovery, IsDiscoveryFailure(err), "discovery failures are distinguishable")
			if tc.status == http.StatusForbidden {
				accept := we.ResponseHeader().Get("Accept-Signature")
				require.NotEmpty(t, accept)
				require.Equal(t, tc.body != "", strings.Contains(accept, `"content-digest"`), "body components only for a request with a body")
			} else {
				require.Empty(t, we.ResponseHeader().Get("Accept-Signature"))
			}
		})
	}
}

func newBody(s string) *readCloser { return &readCloser{strings.NewReader(s)} }

type readCloser struct{ *strings.Reader }

func (readCloser) Close() error { return nil }

func TestWBARequiresVerifierDependencies(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, "")
	for _, opts := range []WBAVerifyOptions{
		{Replay: rig.replay},
		{Keys: rig.resolver},
	} {
		_, err := VerifyWebBotAuthRequest(r, opts)
		require.ErrorIs(t, err, errWBANotConfigured)
	}
}

// TestWBAReplayIsPerOriginAcrossKeyRotation: the identity, not the key,
// scopes a nonce, so rotating keys does not reopen a replay.
func TestWBAReplayIsPerOriginAcrossKeyRotation(t *testing.T) {
	rig := newWBARig(t)
	second := newEd25519(t)
	rig.dirs.publish(t, rig.origin, wbaTestNow.Add(-time.Hour), 24*time.Hour, rig.key, second)
	nonce := "Mn-ZM8SnGESx0-rmyYMuTkJFerfbwEii2C0qNkDP8ugpcuv0pcrt6Da2ifr-6MTmfVeZhnFymLeAofeXJbFaxA"
	sign := func(key ed25519.PrivateKey) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
		require.NoError(t, SignWebBotAuthRequest(r, key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock, NonceOverride: nonce}))
		return r
	}
	_, err := VerifyWebBotAuthRequest(sign(rig.key), rig.opts())
	require.NoError(t, err)
	_, err = VerifyWebBotAuthRequest(sign(rig.key), rig.opts())
	we := requireWBAError(t, err, http.StatusTooManyRequests)
	require.True(t, we.Replayed)
	accept := we.ResponseHeader().Get("Accept-Signature")
	require.Contains(t, accept, `nonce="`)
	require.NotContains(t, accept, nonce, "the verifier supplies a fresh nonce")
	require.NotContains(t, accept, `"content-digest"`, "a bodyless request is asked for the bodyless components")
	_, err = VerifyWebBotAuthRequest(sign(second), rig.opts())
	requireWBAStatus(t, err, http.StatusTooManyRequests)

	// The same nonce from a different identity is not a replay.
	otherKey := newEd25519(t)
	rig.dirs.publish(t, "https://other.example.com", wbaTestNow.Add(-time.Hour), 24*time.Hour, otherKey)
	r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
	require.NoError(t, SignWebBotAuthRequest(r, otherKey, WBASignOptions{AgentOrigin: "https://other.example.com", Clock: wbaClock, NonceOverride: nonce}))
	_, err = VerifyWebBotAuthRequest(r, WBAVerifyOptions{Keys: rig.resolver, Replay: rig.replay, Clock: wbaClock})
	require.NoError(t, err)
}

// TestWBAReplayNamespace: a Web Bot Auth replay identity carries a byte no
// RFC 9421 keyid can (keyids arrive in an HTTP field), so the two profiles
// never share a replay-cache entry or its capacity.
func TestWBAReplayNamespace(t *testing.T) {
	id := wbaReplayIdentity("https://buyer.example.com")
	require.True(t, strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 || r > 0x7e }))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Signature-Input", `sig1=("@method");keyid="`+id+`"`)
	resp, err := srv.Client().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	require.Error(t, err, "net/http refuses the control character in a field value")
}

func TestWBAReplayCapacityIs429(t *testing.T) {
	rig := newWBARig(t)
	rig.replay.MarkKeyIDAtCap(wbaReplayIdentity(rig.origin))
	_, err := VerifyWebBotAuthRequest(rig.signedRequest(t, ""), rig.opts())
	we := requireWBAError(t, err, http.StatusTooManyRequests)
	require.False(t, we.Replayed)
	require.Empty(t, we.ResponseHeader().Get("Accept-Signature"), "capacity is not a replay: no new nonce")
	require.Equal(t, "60", we.ResponseHeader().Get("Retry-After"))
}

// TestWBANoReplayStateOnLaterFailure: a nonce is recorded only once the
// signature verified, so a request that fails verification leaves no replay
// state behind.
func TestWBANoReplayStateOnLaterFailure(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, `{"a":1}`)
	r.Body = newBody(`{"a":2}`)
	_, err := VerifyWebBotAuthRequest(r, rig.opts())
	requireWBAStatus(t, err, http.StatusForbidden)
	in, _ := parseSFDictionary(r.Header.Get("Signature-Input"))
	m, _ := in.get("sig1")
	nonce, _ := stringParam(m.params, "nonce")
	require.False(t, rig.replay.Seen(wbaReplayIdentity(rig.origin), nonce))
}

func TestWBASignerOptions(t *testing.T) {
	key := newEd25519(t)
	jwk, _ := PublicDirectoryKey(key)

	// KeyID, when given, must be the thumbprint.
	_, err := NewSigner(SignerOptions{KeyID: "operator-chosen", PrivateKey: key, AgentOrigin: "https://buyer.example.com"})
	require.Error(t, err)
	s, err := NewSigner(SignerOptions{PrivateKey: key, AgentOrigin: "https://buyer.example.com"})
	require.NoError(t, err)
	require.Equal(t, ProfileTagWebBotAuth, s.SigningProfile())
	_, err = NewSigner(SignerOptions{PrivateKey: key, AgentOrigin: "http://buyer.example.com"})
	require.Error(t, err)

	// Header emission is opt-in: the RFC 9421 signer never sets or covers
	// Signature-Agent.
	legacy, err := NewSigner(SignerOptions{KeyID: "k", PrivateKey: key})
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/x", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	require.NoError(t, legacy.SignRequest(r, SignOptions{CoverContentDigest: true}))
	require.Empty(t, r.Header.Get("Signature-Agent"))
	require.NotContains(t, r.Header.Get("Signature-Input"), "signature-agent")
	require.Equal(t, ProfileRequestSigning.Tag, legacy.SigningProfile())

	// The WBA signer overwrites a stale digest.
	r = httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(`{"a":1}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header["content-digest"] = []string{"sha-256=:AAAA:"}
	r.Header.Set("Content-Digest", "sha-256=:AAAA:")
	delete(r.Header, "content-digest")
	require.NoError(t, s.SignRequest(r, SignOptions{}))
	require.NotEqual(t, "sha-256=:AAAA:", r.Header.Get("Content-Digest"))
	require.Contains(t, r.Header.Get("Signature-Input"), `keyid="`+jwk.Kid+`"`)
	require.Contains(t, r.Header.Get("Signature-Input"), `tag="web-bot-auth"`)

	// A body requires Content-Type, and a signed request cannot be signed
	// again: one signature per request.
	r = httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(`{}`))
	require.Error(t, SignWebBotAuthRequest(r, key, WBASignOptions{AgentOrigin: "https://buyer.example.com"}))
	r.Header.Set("Content-Type", "application/json")
	require.NoError(t, SignWebBotAuthRequest(r, key, WBASignOptions{AgentOrigin: "https://buyer.example.com"}))
	require.Error(t, SignWebBotAuthRequest(r, key, WBASignOptions{AgentOrigin: "https://buyer.example.com"}))
	require.Error(t, SignWebBotAuthRequest(httptest.NewRequest(http.MethodGet, "https://s.example.com/", nil), key,
		WBASignOptions{AgentOrigin: "https://buyer.example.com", ValidityWindow: 301 * time.Second}))
	// The Signer refuses to clobber another party's signature too.
	signed := httptest.NewRequest(http.MethodGet, "https://s.example.com/", nil)
	signed.Header.Set("Signature", "other=:AAAA:")
	require.Error(t, s.SignRequest(signed, SignOptions{}))
	require.Error(t, SignWebBotAuthRequest(httptest.NewRequest(http.MethodGet, "https://s.example.com/", nil), key,
		WBASignOptions{AgentOrigin: "https://buyer.example.com", NonceOverride: "short"}))
}

// TestRFC9421VerifierNeverAttributesWBA: the RFC 9421 verifier never
// accepts a Web Bot Auth signature, so it cannot attribute one.
func TestRFC9421VerifierNeverAttributesWBA(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, `{"a":1}`)
	resolver := NewStaticJWKSResolver()
	jwk, _ := PublicDirectoryKey(rig.key)
	jwk.KeyOps = []string{"verify"}
	jwk.AdcpUse = "request-signing"
	resolver.Put(jwk.Kid, &jwk, rig.origin)
	signer, err := VerifyRequestSignature(r, VerifyOptions{Resolver: resolver, Replay: NewMemoryReplayStore(0), Clock: wbaClock})
	require.Error(t, err)
	require.Nil(t, signer)
}

func TestSignedHTTPClientRequiresAdvertisedRFC9421(t *testing.T) {
	key := newEd25519(t)
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	for _, tc := range []struct {
		profiles []string
		wantErr  bool
	}{{nil, false}, {[]string{ProfileTagRequestSigning, ProfileTagWebBotAuth}, false}, {[]string{ProfileTagWebBotAuth}, true}} {
		client, err := NewSignedHTTPClient(SignedHTTPClientOptions{KeyID: "k", PrivateKey: key, Inner: inner,
			CapabilityProvider: alwaysSignCapability, ProfilesProvider: func(*http.Request) []string { return tc.profiles }})
		require.NoError(t, err)
		req, _ := http.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if tc.wantErr {
			require.ErrorIs(t, err, ErrRequestSigningNotAdvertised)
			continue
		}
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
}

func TestSignedHTTPClientRequiresAdvertisedWBA(t *testing.T) {
	key := newEd25519(t)
	var sent int
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	for _, tc := range []struct {
		name     string
		profiles []string
		wantErr  bool
	}{
		{"profile not advertised", []string{ProfileTagRequestSigning}, true},
		{"profiles omitted means RFC 9421 only", nil, true},
		{"profile advertised", []string{ProfileTagRequestSigning, ProfileTagWebBotAuth}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent = 0
			client, err := NewSignedHTTPClient(SignedHTTPClientOptions{
				PrivateKey:         key,
				AgentOrigin:        "https://buyer.example.com",
				Inner:              inner,
				CapabilityProvider: alwaysSignCapability,
				ProfilesProvider:   func(*http.Request) []string { return tc.profiles },
			})
			require.NoError(t, err)
			req, _ := http.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrWebBotAuthNotAdvertised)
				require.Zero(t, sent, "nothing is sent on a profile mismatch")
				return
			}
			require.NoError(t, err)
			_ = resp.Body.Close()
			require.Equal(t, 1, sent)
		})
	}
}

func alwaysSignCapability(*http.Request) *adcp.RequestSigningCapabilities {
	return &adcp.RequestSigningCapabilities{Supported: true, RequiredFor: []string{"create_media_buy"}}
}

// TestWBAConcurrentReplay: of many concurrent copies of one signed
// request, exactly one is accepted.
func TestWBAConcurrentReplay(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, "")
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := VerifyWebBotAuthRequest(r.Clone(context.Background()), rig.opts()); err == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), accepted.Load())
}

func TestMemoryReplayStoreClaim(t *testing.T) {
	m := NewMemoryReplayStore(2)
	require.Equal(t, ReplayClaimed, m.Claim("k", "n1", time.Minute))
	require.Equal(t, ReplayReplayed, m.Claim("k", "n1", time.Minute))
	require.Equal(t, ReplayClaimed, m.Claim("k", "n2", time.Minute))
	require.Equal(t, ReplayCapacity, m.Claim("k", "n3", time.Minute))
	require.Equal(t, ReplayClaimed, m.Claim("other", "n1", time.Minute))
}

func TestWBAAuthorities(t *testing.T) {
	rig := newWBARig(t)
	opts := rig.opts()
	opts.Authorities = []string{"seller.example.com:443", "api.seller.example.com"}
	_, err := VerifyWebBotAuthRequest(rig.signedRequest(t, ""), opts)
	require.NoError(t, err)
	// A request signed for another seller, replayed here with that seller's
	// Host, is refused even though the signature is valid.
	r := httptest.NewRequest(http.MethodGet, "https://other-seller.example.com/adcp/get_principal", nil)
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	_, err = VerifyWebBotAuthRequest(r, opts)
	requireWBAStatus(t, err, http.StatusForbidden)
}

// TestWBAReadsOnlyTheWebBotAuthSignature: a second signature with another
// tag, such as one a CDN adds, is not read, even when it does not verify.
func TestWBAReadsOnlyTheWebBotAuthSignature(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, `{"jsonrpc":"2.0","id":1}`)
	r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+`, cdn=("@method" "@authority");created=1;keyid="edge";tag="cdn-edge"`)
	r.Header.Set("Signature", r.Header.Get("Signature")+", cdn=:AAAA:")
	signer, err := VerifyWebBotAuthRequest(r, rig.opts())
	require.NoError(t, err)
	require.Equal(t, "sig1", signer.Label)
}

// TestWBASignatureAgentIsExactlyAnOrigin: the Signature-Agent member value
// is the signer's origin, an https origin with no path (WBA §5.2.1 and
// §5.5), spelled exactly as its canonical form. Any other spelling, a path,
// a query, a fragment, or userinfo is refused with 403 before any directory
// is fetched. Each refused request carries a valid signature.
func TestWBASignatureAgentIsExactlyAnOrigin(t *testing.T) {
	rig := newWBARig(t)
	signer, err := VerifyWebBotAuthRequest(rig.signWithAgent(t, rig.origin), rig.opts())
	require.NoError(t, err)
	require.Equal(t, rig.origin, signer.AgentURL)

	for name, value := range map[string]string{
		"trailing slash":           rig.origin + "/",
		"path":                     rig.origin + "/agents/a",
		"the well-known URI":       rig.origin + DirectoryPath,
		"query":                    rig.origin + "?v=1",
		"fragment":                 rig.origin + "#k",
		"uppercase host":           "https://BUYER.example.com",
		"default port spelled out": "https://buyer.example.com:443",
		"userinfo":                 "https://u@buyer.example.com",
		"http":                     "http://buyer.example.com",
		// A port with a leading zero survived CanonicalOrigin as a second
		// spelling of the identity; WHATWG (the judge) refuses it.
		"default port with a leading zero": "https://buyer.example.com:0443",
		"port with a leading zero":         "https://buyer.example.com:08443",
		"port out of range":                "https://buyer.example.com:99999",
		"port zero":                        "https://buyer.example.com:0",
	} {
		t.Run(name, func(t *testing.T) {
			opts := rig.opts()
			opts.Keys = NewDirectoryResolver(DirectoryResolverOptions{Fetch: rig.dirs.fetch, Clock: wbaClock})
			before := rig.dirs.count(rig.origin)
			_, err := VerifyWebBotAuthRequest(rig.signWithAgent(t, value), opts)
			we := requireWBAError(t, err, http.StatusForbidden)
			require.False(t, we.DiscoveryFailure)
			require.Equal(t, before, rig.dirs.count(rig.origin), "refused before any directory fetch")
		})
	}
}

// TestWBAOriginPortIsCanonical: a Web Bot Auth origin's port is decimal,
// 1 to 65535, with no leading zero, wherever an origin is named (signer,
// directory, revocation, Signature-Agent), so one identity has one
// spelling.
func TestWBAOriginPortIsCanonical(t *testing.T) {
	for _, ok := range []string{"https://buyer.example.com", "https://buyer.example.com:8443", "https://buyer.example.com:1", "https://buyer.example.com:65535"} {
		o, err := webBotAuthOrigin(ok)
		require.NoError(t, err, ok)
		require.Equal(t, ok, o)
	}
	o, err := webBotAuthOrigin("https://buyer.example.com:443")
	require.NoError(t, err)
	require.Equal(t, "https://buyer.example.com", o, "the default port is stripped")
	key := newWBARig(t).key
	for _, bad := range []string{"https://buyer.example.com:0443", "https://buyer.example.com:08443", "https://buyer.example.com:00443", "https://buyer.example.com:0", "https://buyer.example.com:65536", "https://buyer.example.com:99999"} {
		_, err := webBotAuthOrigin(bad)
		require.Error(t, err, bad)
		_, err = SignKeyDirectory(bad, []any{key}, wbaTestNow, time.Hour)
		require.Error(t, err, "the signer refuses %s too", bad)
	}
}

// TestWBAMalformedSignatureMemberIs400: RFC 9421 §4.2 defines every
// Signature member as an unparameterized byte sequence and every
// Signature-Input member as an inner list. A member that is not makes the
// field malformed, 400, under any label, as Cloudflare's http-message-sig
// answers (MalformedField). Nothing else is read from a sibling member.
func TestWBAMalformedSignatureMemberIs400(t *testing.T) {
	const sibling = `cdn=("@method" "@authority");created=1;keyid="edge";tag="cdn-edge"`
	for _, tc := range []struct {
		name, input, signature string
	}{
		{"sibling Signature member is a token", sibling, "cdn=token"},
		{"sibling Signature member is an inner list", sibling, "cdn=(1 2)"},
		{"sibling Signature member has a parameter", sibling, "cdn=:AAAA:;x=1"},
		{"sibling Signature-Input member is a token", `cdn=token;tag="cdn-edge"`, "cdn=:AAAA:"},
		{"sibling Signature-Input member is a token tagged web-bot-auth", `cdn=token;tag="web-bot-auth"`, "cdn=:AAAA:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWBARig(t)
			r := rig.signedRequest(t, `{"jsonrpc":"2.0","id":1}`)
			r.Header.Set("Signature-Input", r.Header.Get("Signature-Input")+", "+tc.input)
			r.Header.Set("Signature", r.Header.Get("Signature")+", "+tc.signature)
			_, err := VerifyWebBotAuthRequest(r, rig.opts())
			requireWBAStatus(t, err, http.StatusBadRequest)
		})
	}

	// A malformed web-bot-auth Signature-Input member is 400, not "no
	// web-bot-auth signature": the Signature-Input check runs before the
	// member is chosen. So is a malformed member when the request carries
	// no web-bot-auth member at all.
	for name, input := range map[string]string{ // #nosec G101 -- Signature-Input fixtures, no credentials.
		"web-bot-auth Signature-Input member is a token":  `sig1=token;tag="web-bot-auth"`,
		"Signature-Input token beside an RFC 9421 member": `sig1=("@method");created=1;keyid="k";tag="adcp/request-signing/v1", cdn=token`,
	} {
		t.Run(name, func(t *testing.T) {
			rig := newWBARig(t)
			r := rig.signedRequest(t, `{"jsonrpc":"2.0","id":1}`)
			r.Header.Set("Signature-Input", input)
			sig := "sig1=:AAAA:"
			if strings.Contains(input, "cdn=") {
				sig += ", cdn=:AAAA:"
			}
			r.Header.Set("Signature", sig)
			_, err := VerifyWebBotAuthRequest(r, rig.opts())
			requireWBAStatus(t, err, http.StatusBadRequest)
		})
	}

	// The Signature half keeps #571's order: a request with no web-bot-auth
	// member gets 403 with Accept-Signature before Signature is parsed, so
	// an AdCP 3.0 or 3.1 signer whose unpadded base64url Signature is not
	// an RFC 8941 Byte Sequence is told to switch profiles.
	t.Run("malformed Signature with no web-bot-auth member stays 403", func(t *testing.T) {
		rig := newWBARig(t)
		r := rig.signedRequest(t, `{"jsonrpc":"2.0","id":1}`)
		r.Header.Set("Signature-Input", `sig1=("@method");created=1;keyid="k";tag="adcp/request-signing/v1"`)
		r.Header.Set("Signature", "sig1=token")
		_, err := VerifyWebBotAuthRequest(r, rig.opts())
		we := requireWBAError(t, err, http.StatusForbidden)
		require.NotEmpty(t, we.ResponseHeader().Get("Accept-Signature"))
	})

	for name, mutate := range map[string]func(string) string{
		"web-bot-auth Signature member is a token":       func(string) string { return "sig1=token" },
		"web-bot-auth Signature member has a parameter":  func(s string) string { return s + ";x=1" },
		"web-bot-auth Signature member is an inner list": func(string) string { return "sig1=(1 2)" },
	} {
		t.Run(name, func(t *testing.T) {
			rig := newWBARig(t)
			r := rig.signedRequest(t, `{"jsonrpc":"2.0","id":1}`)
			r.Header.Set("Signature", mutate(r.Header.Get("Signature")))
			_, err := VerifyWebBotAuthRequest(r, rig.opts())
			requireWBAStatus(t, err, http.StatusBadRequest)
		})
	}
}

// replaceNonce swaps the nonce parameter of a serialized Signature-Input.
func replaceNonce(input, nonce string) string {
	start := strings.Index(input, `nonce="`) + len(`nonce="`)
	end := start + strings.Index(input[start:], `"`)
	return input[:start] + nonce + input[end:]
}

// TestWBANonceEncodings: this signer emits 64 random bytes as unpadded
// base64url (the profile states no preference for signers); the verifier
// accepts exactly 64 bytes as unpadded base64url or padded standard base64,
// the two forms Cloudflare's web-bot-auth validateNonce accepts. Padded
// standard base64 is the encoding its generateNonce and the WBA
// architecture draft's Appendix A vectors use. Padded base64url is refused,
// as validateNonce refuses it.
func TestWBANonceEncodings(t *testing.T) {
	rig := newWBARig(t)
	r := rig.signedRequest(t, "")
	in, err := parseSFDictionary(r.Header.Get("Signature-Input"))
	require.NoError(t, err)
	m, _ := in.get("sig1")
	nonce, _ := stringParam(m.params, "nonce")
	require.Len(t, nonce, 86)
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	require.NoError(t, err)
	require.Len(t, raw, 64)

	std := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xff}, 32)) // carries '+', '/', '='
	require.Contains(t, std, "+")
	r = httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/get_principal", nil)
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock, NonceOverride: std}))
	_, err = VerifyWebBotAuthRequest(r, rig.opts())
	require.NoError(t, err, "padded standard base64 is accepted")

	paddedURL := base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb, 0xff}, 32))
	require.Contains(t, paddedURL, "_")
	require.True(t, strings.HasSuffix(paddedURL, "=="))

	for _, bad := range []string{
		base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		base64.RawURLEncoding.EncodeToString(make([]byte, 63)),
		base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 64)),
		paddedURL,
	} {
		require.False(t, validWBANonce(bad), bad)
		require.Error(t, SignWebBotAuthRequest(httptest.NewRequest(http.MethodGet, "https://s.example.com/", nil), rig.key,
			WBASignOptions{AgentOrigin: rig.origin, NonceOverride: bad}))
	}
}

// TestWBATargetURIAsReceived: @target-uri is the target URI as received
// (RFC 9421 §2.2.2), percent-encoding included, so a signer on another HTTP
// stack that signs the URI it sent verifies; @authority is normalized.
func TestWBATargetURIAsReceived(t *testing.T) {
	rig := newWBARig(t)
	const sent = "https://seller.example.com/a%7Eb/mcp/?q=%7e&x=a+b"
	client := httptest.NewRequest(http.MethodGet, sent, nil)
	require.NoError(t, SignWebBotAuthRequest(client, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	received := func(signed *http.Request, requestTarget, host string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "https://"+host+requestTarget, nil)
		r.RequestURI = requestTarget
		r.Host = host
		for _, h := range []string{"Signature", "Signature-Input", "Signature-Agent"} {
			r.Header.Set(h, signed.Header.Get(h))
		}
		return r
	}
	server := received(client, "/a%7Eb/mcp/?q=%7e&x=a+b", "seller.example.com")
	in, _ := parseSFDictionary(server.Header.Get("Signature-Input"))
	m, _ := in.get("sig1")
	target, ok := receivedTargetURI(server, "https")
	require.True(t, ok)
	base, err := wbaSignatureBase(server.Method, target, server.Header, m, false)
	require.NoError(t, err)
	require.Contains(t, base, `"@target-uri": `+sent+"\n", "not canonicalized")
	_, err = VerifyWebBotAuthRequest(server, rig.opts())
	require.NoError(t, err)

	// A signature over the decoded path does not verify against the
	// encoded request-target, and vice versa: no canonicalization either way.
	decoded := httptest.NewRequest(http.MethodGet, "https://seller.example.com/a~b/mcp/?q=%7e&x=a+b", nil)
	require.NoError(t, SignWebBotAuthRequest(decoded, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	_, err = VerifyWebBotAuthRequest(received(decoded, "/a%7Eb/mcp/?q=%7e&x=a+b", "seller.example.com"), rig.opts())
	requireWBAStatus(t, err, http.StatusForbidden)

	// @authority is normalized (RFC 9421 §2.2.3): a received Host with
	// upper case and the default port verifies a signature over the
	// normalized authority, since both sides normalize it.
	explicit := httptest.NewRequest(http.MethodGet, "https://Seller.Example.com:443/x", nil)
	require.NoError(t, SignWebBotAuthRequest(explicit, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	_, err = VerifyWebBotAuthRequest(received(explicit, "/x", "Seller.Example.com:443"), rig.opts())
	require.NoError(t, err)
}

type fakeRevocationStates map[string]RevocationState

func (f fakeRevocationStates) CheckWBAKey(_ context.Context, origin, _ string) (RevocationState, error) {
	return f[origin], nil
}

// TestCheckWBARevocation: revocation runs after verification, records the
// signer's state, and refuses a revoked key with 403 and no
// Accept-Signature.
func TestCheckWBARevocation(t *testing.T) {
	signer := &VerifiedSigner{AgentURL: "https://a.example.com", Label: "a"}
	require.NoError(t, CheckWBARevocation(context.Background(), fakeRevocationStates{"https://a.example.com": RevocationStale}, signer))
	require.Equal(t, RevocationStale, signer.Revocation)
	require.NoError(t, CheckWBARevocation(context.Background(), fakeRevocationStates{"https://a.example.com": RevocationNoList}, signer))
	require.Equal(t, RevocationNoList, signer.Revocation)
	err := CheckWBARevocation(context.Background(), fakeRevocationStates{"https://a.example.com": RevocationRevoked}, signer)
	we := requireWBAError(t, err, http.StatusForbidden)
	require.True(t, we.Revoked)
	require.Equal(t, "a", we.Label)
	require.Empty(t, we.ResponseHeader().Get("Accept-Signature"))
	require.False(t, IsDiscoveryFailure(err))
}

// TestWBAAbsoluteFormTargetMustMatchConnection: an absolute-form
// request-target is used as received only when its scheme and authority
// match the connection, so a signature captured over one scheme cannot be
// replayed over another.
func TestWBAAbsoluteFormTargetMustMatchConnection(t *testing.T) {
	rig := newWBARig(t)
	r := httptest.NewRequest(http.MethodGet, "http://seller.example.com/x", nil)
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	opts := rig.opts()
	opts.Scheme = "https" // the connection is TLS; the signed target says http
	_, err := VerifyWebBotAuthRequest(r, opts)
	requireWBAStatus(t, err, http.StatusForbidden)

	r = httptest.NewRequest(http.MethodGet, "https://seller.example.com/x", nil)
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	r.Host = "other.example.com"
	_, err = VerifyWebBotAuthRequest(r, rig.opts())
	requireWBAStatus(t, err, http.StatusForbidden)
}
