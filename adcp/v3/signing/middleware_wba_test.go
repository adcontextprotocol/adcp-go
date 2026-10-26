package signing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/stretchr/testify/require"
)

// fakeAuthorizer binds origins to principals and can deny.
type fakeAuthorizer struct {
	principals map[string]string
	deny       map[string]bool
	calls      []string
}

func (f *fakeAuthorizer) AuthorizeWBA(_ context.Context, s *VerifiedSigner, op string) (string, error) {
	f.calls = append(f.calls, op)
	if f.deny[s.AgentURL] {
		return "", errors.New("operator delisted the origin")
	}
	return f.principals[s.AgentURL], nil
}

type fakeBoundOrigins map[string]string

func (f fakeBoundOrigins) PrincipalForOrigin(_ context.Context, origin string) (string, bool, error) {
	p, ok := f[origin]
	return p, ok, nil
}

type captured struct {
	signer *VerifiedSigner
	body   string
	called bool
}

func captureHandler(c *captured) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.called = true
		c.signer = VerifiedSignerFromContext(r.Context())
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		c.body = buf.String()
		w.WriteHeader(http.StatusOK)
	})
}

func mcpCall(tool string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
}

func TestMiddlewareWebBotAuthBindingGate(t *testing.T) {
	const (
		withIdentity = "identity"  // handler runs with the verified signer
		anonymous    = "anonymous" // handler runs as an anonymous request
		refused      = "refused"   // AdCP error envelope, handler not run
	)
	bound := map[string]string{"https://buyer.example.com": "principal_7"}
	cases := []struct {
		name        string
		body        string
		authorizer  WBAAuthorizer
		unboundFor  []string
		requiredFor []string
		want        string
		principal   string
		code        string
	}{
		{"identity-established may call get_principal", mcpCall("get_principal"), &fakeAuthorizer{}, nil, nil, withIdentity, "", ""},
		{"identity-established may call get_adcp_capabilities", mcpCall("get_adcp_capabilities"), nil, nil, nil, withIdentity, "", ""},
		{"beyond UnboundFor: served as an anonymous caller", mcpCall("create_media_buy"), &fakeAuthorizer{}, nil, nil, anonymous, "", ""},
		{"sync_accounts from an unbound origin: anonymous", mcpCall("sync_accounts"), nil, nil, nil, anonymous, "", ""},
		{"beyond UnboundFor on a signature-required operation: PERMISSION_DENIED", mcpCall("create_media_buy"), nil, nil, []string{"create_media_buy"}, refused, "", CodePermissionDenied},
		{"configured anonymous reads", mcpCall("get_products"), &fakeAuthorizer{}, []string{"get_products"}, nil, withIdentity, "", ""},
		{"bound origin gets its principal", mcpCall("create_media_buy"), &fakeAuthorizer{principals: bound}, nil, nil, withIdentity, "principal_7", ""},
		{"authorizer error: PERMISSION_DENIED envelope", mcpCall("create_media_buy"), &fakeAuthorizer{principals: bound, deny: map[string]bool{"https://buyer.example.com": true}}, nil, nil, refused, "", CodePermissionDenied},
		{"authorizer outage on spend: SERVICE_UNAVAILABLE", mcpCall("create_media_buy"), outageAuthorizer{}, nil, nil, refused, "", CodeServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWBARig(t)
			opts := &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, UnboundFor: tc.unboundFor, Clock: wbaClock}
			if tc.authorizer != nil {
				opts.Authorizer = tc.authorizer
			}
			var c captured
			h := Middleware(MiddlewareOptions{WebBotAuth: opts, Replay: rig.replay, RequiredFor: tc.requiredFor,
				OperationResolver: func(*http.Request) string { return "" }, Logger: discardLogger()})(captureHandler(&c))
			r := rig.signedRequest(t, tc.body)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			// A correctly signed request never gets Accept-Signature.
			require.Empty(t, rec.Header().Get("Accept-Signature"))
			switch tc.want {
			case withIdentity:
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.True(t, c.called)
				require.Equal(t, "https://buyer.example.com", c.signer.AgentURL)
				require.Equal(t, tc.principal, c.signer.PrincipalID)
				require.Equal(t, tc.body, c.body, "the handler still reads the body")
			case anonymous:
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.True(t, c.called)
				require.Nil(t, c.signer, "no identity is attached beyond the unbound surface")
				require.Equal(t, tc.body, c.body)
			case refused:
				require.False(t, c.called)
				requireAdCPEnvelope(t, rec, tc.code)
				require.NotContains(t, rec.Body.String(), "delisted", "no detail leaks")
			}
		})
	}
}

type outageAuthorizer struct{}

func (outageAuthorizer) AuthorizeWBA(context.Context, *VerifiedSigner, string) (string, error) {
	return "", NewAuthorizationError(CodeServiceUnavailable, "trust record unreachable", nil)
}

// requireAdCPEnvelope asserts the standard AdCP error envelope with code,
// the matching HTTP status, and no Accept-Signature.
func requireAdCPEnvelope(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	require.Empty(t, rec.Header().Get("Accept-Signature"))
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body struct {
		Errors []struct{ Code, Message, Recovery string } `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.Len(t, body.Errors, 1)
	require.Equal(t, code, body.Errors[0].Code)
	switch code {
	case CodeServiceUnavailable:
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Equal(t, "transient", body.Errors[0].Recovery)
		require.NotEmpty(t, rec.Header().Get("Retry-After"))
	default:
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, "correctable", body.Errors[0].Recovery)
	}
}

// requireGenericAuthError asserts a signature-path failure: the WBA status,
// Accept-Signature only when wanted, and the one generic authentication
// error body (DR-0023).
func requireGenericAuthError(t *testing.T, rec *httptest.ResponseRecorder, status int, acceptSignature bool) {
	t.Helper()
	require.Equal(t, status, rec.Code)
	require.Equal(t, acceptSignature, rec.Header().Get("Accept-Signature") != "", "Accept-Signature")
	require.Empty(t, rec.Header().Get("Retry-After"))
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, string(genericAuthenticationError), rec.Body.String())
}

// TestMiddlewareSignaturePathHasNoOutages: the signature path answers only
// with WBA statuses (DR-0023). A key directory that cannot be resolved is a
// discovery failure (403 with Accept-Signature by default, or anonymous by
// policy), never 503; a revocation list that cannot be read is recorded for
// the authorization layer and does not block verification; a revoked key is
// 403 without Accept-Signature.
func TestMiddlewareSignaturePathHasNoOutages(t *testing.T) {
	serve := func(t *testing.T, rig *wbaRig, opts WebBotAuthOptions, body string, mw MiddlewareOptions) (*httptest.ResponseRecorder, *captured) {
		var c captured
		opts.Keys, opts.Revocation, opts.Clock = rig.resolver, rig.checker, wbaClock
		if opts.Authorizer == nil {
			opts.Authorizer = &fakeAuthorizer{principals: map[string]string{rig.origin: "p"}}
		}
		mw.WebBotAuth, mw.Replay, mw.Logger = &opts, rig.replay, discardLogger()
		rec := httptest.NewRecorder()
		Middleware(mw)(captureHandler(&c)).ServeHTTP(rec, rig.signedRequest(t, body))
		return rec, &c
	}

	t.Run("revocation list unreadable: verified, stale for the authorization layer", func(t *testing.T) {
		for _, op := range []string{"create_media_buy", "get_media_buys"} {
			rig := newWBARig(t)
			rig.revs.fetchErr = errors.New("connection refused")
			rec, c := serve(t, rig, WebBotAuthOptions{}, mcpCall(op), MiddlewareOptions{})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.True(t, c.called, op)
			require.Equal(t, RevocationStale, c.signer.Revocation)
		}
	})
	t.Run("no revocation list: verified, recorded as no_list", func(t *testing.T) {
		rig := newWBARig(t)
		rig.revs.mu.Lock()
		rig.revs.status = http.StatusNotFound
		rig.revs.mu.Unlock()
		rec, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_media_buys"), MiddlewareOptions{})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, RevocationNoList, c.signer.Revocation)
	})
	t.Run("current list", func(t *testing.T) {
		rig := newWBARig(t)
		_, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_media_buys"), MiddlewareOptions{})
		require.Equal(t, RevocationCurrent, c.signer.Revocation)
	})
	t.Run("revoked key: 403 without Accept-Signature", func(t *testing.T) {
		rig := newWBARig(t)
		jwk, _ := PublicDirectoryKey(rig.key)
		rig.revs.set(t, revocationPayload{Updated: rfc3339(wbaTestNow.Add(-time.Minute)), NextUpdate: rfc3339(wbaTestNow.Add(10 * time.Minute)), RevokedKids: []string{jwk.Kid}})
		rec, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_media_buys"), MiddlewareOptions{})
		requireGenericAuthError(t, rec, http.StatusForbidden, false)
		require.False(t, c.called)
	})
	for name, breakDir := range map[string]func(*wbaRig){
		"timeout": func(rig *wbaRig) { rig.dirs.fail(rig.origin, errors.New("i/o timeout")) },
		"404":     func(rig *wbaRig) { rig.dirs.mu.Lock(); delete(rig.dirs.dirs, rig.origin); rig.dirs.mu.Unlock() },
		"503": func(rig *wbaRig) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin] = &DirectoryResponse{Status: 503}
			rig.dirs.mu.Unlock()
		},
		"malformed": func(rig *wbaRig) {
			rig.dirs.mu.Lock()
			rig.dirs.dirs[rig.origin].Body = []byte("{")
			rig.dirs.mu.Unlock()
		},
	} {
		t.Run("directory "+name+": discovery failure rejected by default", func(t *testing.T) {
			rig := newWBARig(t)
			breakDir(rig)
			rec, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_adcp_capabilities"), MiddlewareOptions{})
			requireGenericAuthError(t, rec, http.StatusForbidden, true)
			require.False(t, c.called)
		})
		t.Run("directory "+name+": anonymous by policy", func(t *testing.T) {
			rig := newWBARig(t)
			breakDir(rig)
			rec, c := serve(t, rig, WebBotAuthOptions{OnDiscoveryFailure: DiscoveryFailureAnonymous}, mcpCall("get_adcp_capabilities"), MiddlewareOptions{})
			require.Equal(t, http.StatusOK, rec.Code)
			require.True(t, c.called)
			require.Nil(t, c.signer, "an unverified request carries no signer")
			// An operation that requires a signature is still refused.
			rig = newWBARig(t)
			breakDir(rig)
			rec, c = serve(t, rig, WebBotAuthOptions{OnDiscoveryFailure: DiscoveryFailureAnonymous}, mcpCall("create_media_buy"), MiddlewareOptions{RequiredFor: []string{"create_media_buy"}})
			requireGenericAuthError(t, rec, http.StatusForbidden, true)
			require.False(t, c.called)
		})
	}
	t.Run("cached directory keeps verifying through an outage", func(t *testing.T) {
		rig := newWBARig(t)
		_, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_media_buys"), MiddlewareOptions{})
		require.True(t, c.called)
		rig.dirs.fail(rig.origin, errors.New("i/o timeout"))
		rec, c := serve(t, rig, WebBotAuthOptions{}, mcpCall("get_media_buys"), MiddlewareOptions{})
		require.Equal(t, http.StatusOK, rec.Code)
		require.NotNil(t, c.signer)
	})
}

func TestMiddlewareWebBotAuthFailuresUseStatusCodes(t *testing.T) {
	rig := newWBARig(t)
	var c captured
	h := Middleware(MiddlewareOptions{
		WebBotAuth:  &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, Clock: wbaClock},
		Replay:      rig.replay,
		ObserveOnly: true, // a WBA failure rejects even in shadow mode
		Logger:      discardLogger(),
	})(captureHandler(&c))

	r := rig.signedRequest(t, mcpCall("get_principal"))
	r.Header.Set("Signature-Agent", `sig1="https://attacker.example.com"`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireGenericAuthError(t, rec, http.StatusForbidden, true)
	require.Empty(t, rec.Header().Get("WWW-Authenticate"), "no request_signature_* codes under Web Bot Auth")
	require.NotContains(t, rec.Body.String(), "request_signature")
	require.False(t, c.called)

	r = rig.signedRequest(t, mcpCall("get_principal"))
	r.Header.Set("Signature-Input", "garbage(")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireGenericAuthError(t, rec, http.StatusBadRequest, false)

	// Custom rejection rendering.
	var seen *WBAError
	h2 := Middleware(MiddlewareOptions{
		WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, Clock: wbaClock, OnReject: func(w http.ResponseWriter, _ *http.Request, e *WBAError) {
			seen = e
			w.WriteHeader(e.Status)
		}},
		Replay: rig.replay, Logger: discardLogger(),
	})(captureHandler(&c))
	jwk, _ := PublicDirectoryKey(rig.key)
	rig.revs.set(t, revocationPayload{Updated: rfc3339(wbaTestNow.Add(-time.Minute)), NextUpdate: rfc3339(wbaTestNow.Add(10 * time.Minute)), RevokedKids: []string{jwk.Kid}})
	r = rig.signedRequest(t, mcpCall("get_principal"))
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, r)
	require.NotNil(t, seen)
	require.True(t, seen.Revoked)
	require.False(t, seen.Authorization, "a revoked key is a signature-path failure")
	require.Empty(t, seen.ResponseHeader().Get("Accept-Signature"))
}

// TestMiddlewareWebBotAuthOneSignature: a request carrying a second
// web-bot-auth signature, from an intermediary or anyone else, is refused
// with 403 and Accept-Signature even when both signatures verify and either
// origin is bound. An intermediary that signs is the signer.
func TestMiddlewareWebBotAuthOneSignature(t *testing.T) {
	rig := newWBARig(t)
	relayKey := newEd25519(t)
	relay := "https://relay.agency.example.com"
	rig.dirs.publish(t, relay, wbaTestNow.Add(-time.Hour), 24*time.Hour, relayKey)
	auth := &fakeAuthorizer{principals: map[string]string{relay: "relay_principal", rig.origin: "brand_principal"}}
	var c captured
	h := Middleware(MiddlewareOptions{WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Authorizer: auth, Clock: wbaClock}, Logger: discardLogger()})(captureHandler(&c))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, twoSignatureRequest(t, rig, relayKey, relay, mcpCall("create_media_buy")))
	requireGenericAuthError(t, rec, http.StatusForbidden, true)
	require.False(t, c.called)
	require.Empty(t, auth.calls, "no authorization runs for a request the profile refuses")
	require.True(t, strings.HasPrefix(rec.Header().Get("Accept-Signature"), "sig1="), rec.Header().Get("Accept-Signature"))
}

// twoSignatureRequest is a request signed by rig's key under sig1 and by
// secondKey for secondOrigin under sig2, each signature valid on its own.
func twoSignatureRequest(t *testing.T, rig *wbaRig, secondKey any, secondOrigin, body string) *http.Request {
	t.Helper()
	mk := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	first, second := mk(), mk()
	require.NoError(t, SignWebBotAuthRequest(first, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	require.NoError(t, SignWebBotAuthRequest(second, secondKey, WBASignOptions{AgentOrigin: secondOrigin, Label: "sig2", Clock: wbaClock}))
	for _, h := range []string{"Signature-Agent", "Signature-Input", "Signature"} {
		first.Header.Set(h, first.Header.Get(h)+", "+second.Header.Get(h))
	}
	return first
}

// TestMiddlewareWebBotAuthReplayIs429: a reused signature is answered 429
// with Accept-Signature carrying a fresh 64-byte nonce (WBA §5.3).
func TestMiddlewareWebBotAuthReplayIs429(t *testing.T) {
	rig := newWBARig(t)
	var c captured
	h := Middleware(MiddlewareOptions{WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Clock: wbaClock}, Replay: rig.replay, Logger: discardLogger()})(captureHandler(&c))
	r := rig.signedRequest(t, mcpCall("get_principal"))
	replayed := r.Clone(context.Background())
	replayed.Body = io.NopCloser(strings.NewReader(mcpCall("get_principal")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
	c = captured{}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, replayed)
	requireGenericAuthError(t, rec, http.StatusTooManyRequests, true)
	require.False(t, c.called)
	d, err := parseSFDictionary(rec.Header().Get("Accept-Signature"))
	require.NoError(t, err)
	m, ok := d.get("sig1")
	require.True(t, ok)
	nonce, ok := stringParam(m.params, "nonce")
	require.True(t, ok, "a replay offers a fresh nonce")
	require.True(t, validWBANonce(nonce))
}

// TestMiddlewareUnsignedRequiredFor: an unsigned request to an operation in
// required_for gets the answer the profile gives for what the verifier
// advertises. With both profiles, the RFC 9421 answer (401
// request_signature_required) plus Accept-Signature for Web Bot Auth; with
// Web Bot Auth only (Exclusive), 403 with Accept-Signature.
func TestMiddlewareUnsignedRequiredFor(t *testing.T) {
	rig := newWBARig(t)
	unsigned := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	build := func(exclusive, observe bool, onReject func(http.ResponseWriter, *http.Request, *Error)) (http.Handler, *captured) {
		var c captured
		return Middleware(MiddlewareOptions{
			Resolver:          NewStaticJWKSResolver(),
			WebBotAuth:        &WebBotAuthOptions{Keys: rig.resolver, Clock: wbaClock, Exclusive: exclusive},
			Replay:            rig.replay,
			RequiredFor:       []string{"create_media_buy"},
			OperationResolver: func(r *http.Request) string { return jsonRPCOperation(r, 0) },
			ObserveOnly:       observe,
			OnReject:          onReject,
			Logger:            discardLogger(),
		})(captureHandler(&c)), &c
	}

	t.Run("both profiles: 401 request_signature_required plus Accept-Signature", func(t *testing.T) {
		h, c := build(false, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("create_media_buy")))
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Equal(t, `Signature error="request_signature_required"`, rec.Header().Get("WWW-Authenticate"))
		require.Equal(t, AcceptSignature(true), rec.Header().Get("Accept-Signature"))
		require.False(t, c.called)
	})
	t.Run("both profiles, custom OnReject: Accept-Signature is still set", func(t *testing.T) {
		h, _ := build(false, false, func(w http.ResponseWriter, _ *http.Request, e *Error) { w.WriteHeader(http.StatusTeapot) })
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("create_media_buy")))
		require.Equal(t, http.StatusTeapot, rec.Code)
		require.NotEmpty(t, rec.Header().Get("Accept-Signature"))
	})
	t.Run("Web Bot Auth only: 403 with Accept-Signature", func(t *testing.T) {
		h, c := build(true, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("create_media_buy")))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.Equal(t, AcceptSignature(true), rec.Header().Get("Accept-Signature"))
		require.Empty(t, rec.Header().Get("WWW-Authenticate"))
		require.False(t, c.called)
	})
	t.Run("Web Bot Auth only: a bodyless request is asked for the bodyless components", func(t *testing.T) {
		var c captured
		h := Middleware(MiddlewareOptions{
			WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Clock: wbaClock, Exclusive: true},
			Replay:     rig.replay, RequiredFor: []string{"create_media_buy"}, OperationResolver: DefaultOperationResolver, Logger: discardLogger(),
		})(captureHandler(&c))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/create_media_buy", nil))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.Equal(t, AcceptSignature(false), rec.Header().Get("Accept-Signature"))
		require.NotContains(t, rec.Header().Get("Accept-Signature"), "content-digest")
	})
	t.Run("Web Bot Auth only: push_notification_config.authentication needs a signature", func(t *testing.T) {
		h, c := build(true, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_media_buys","arguments":{"push_notification_config":{"url":"https://b.example.com/h","authentication":{"schemes":["Bearer"],"credentials":"x"}}}}}`))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.False(t, c.called)
	})
	t.Run("Web Bot Auth only: an operation outside required_for proceeds unsigned", func(t *testing.T) {
		h, c := build(true, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("get_products")))
		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, c.called)
		require.Nil(t, c.signer)
		require.Equal(t, mcpCall("get_products"), c.body)
	})
	t.Run("Web Bot Auth only, ObserveOnly: allowed through", func(t *testing.T) {
		h, c := build(true, true, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("create_media_buy")))
		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, c.called)
	})
	t.Run("Web Bot Auth only: an RFC 9421 signature is not accepted", func(t *testing.T) {
		res, err := GenerateSigningKey(AlgEd25519, "legacy-key")
		require.NoError(t, err)
		priv, _, _ := LoadPrivateKey(res.PrivateKeyPEM)
		legacy, err := NewSigner(SignerOptions{KeyID: "legacy-key", PrivateKey: priv})
		require.NoError(t, err)
		r := unsigned(mcpCall("get_products"))
		require.NoError(t, legacy.SignRequest(r, SignOptions{CoverContentDigest: true}))
		h, c := build(true, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		// 403 with Accept-Signature, whatever bytes the RFC 9421
		// Signature (unpadded base64url, not RFC 8941) happens to carry.
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.Empty(t, rec.Header().Get("WWW-Authenticate"))
		require.False(t, c.called)
	})
	t.Run("Web Bot Auth only: a Web Bot Auth signature verifies", func(t *testing.T) {
		h, c := build(true, false, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rig.signedRequest(t, mcpCall("get_principal")))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, c.signer)
	})
}

func TestMiddlewareRFC9421Coexistence(t *testing.T) {
	res, err := GenerateSigningKey(AlgEd25519, "legacy-key")
	require.NoError(t, err)
	priv, _, _ := LoadPrivateKey(res.PrivateKeyPEM)
	resolver := NewStaticJWKSResolver()
	jwk := res.PublicJWK
	resolver.Put("legacy-key", &jwk, "https://buyer.example.com/mcp")
	legacy, err := NewSigner(SignerOptions{KeyID: "legacy-key", PrivateKey: priv})
	require.NoError(t, err)

	sign := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/create_media_buy", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		require.NoError(t, legacy.SignRequest(r, SignOptions{CoverContentDigest: true}))
		return r
	}
	rig := newWBARig(t)
	wba := &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, Clock: wbaClock}

	// RFC 9421 still works with Web Bot Auth enabled.
	var c captured
	h := Middleware(MiddlewareOptions{Resolver: resolver, WebBotAuth: wba, SchemeOverride: "https", Logger: discardLogger()})(captureHandler(&c))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, sign())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, ProfileRequestSigning.Tag, c.signer.Profile)

	// No downgrade: once the origin is bound, RFC 9421 from it is refused,
	// with or without Web Bot Auth enabled.
	for _, w := range []*WebBotAuthOptions{wba, nil} {
		c = captured{}
		h = Middleware(MiddlewareOptions{Resolver: resolver, WebBotAuth: w, SchemeOverride: "https", Logger: discardLogger(),
			BoundOrigins: fakeBoundOrigins{"https://buyer.example.com": "principal_7"}})(captureHandler(&c))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, sign())
		require.Equal(t, http.StatusForbidden, rec.Code)
		require.Contains(t, rec.Body.String(), "PERMISSION_DENIED")
		require.False(t, c.called)
	}

	// An RFC 9421 signature carrying an uncovered Signature-Agent is
	// verified under Web Bot Auth only when it is enabled, and fails there
	// with 403 and Accept-Signature: it has no web-bot-auth member, which
	// is decided before its base64url Signature is parsed.
	r := sign()
	r.Header.Set("Signature-Agent", `sig1="https://buyer.example.com"`)
	c = captured{}
	h = Middleware(MiddlewareOptions{Resolver: resolver, WebBotAuth: wba, SchemeOverride: "https", Logger: discardLogger()})(captureHandler(&c))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	requireGenericAuthError(t, rec, http.StatusForbidden, true)
	require.Empty(t, rec.Header().Get("WWW-Authenticate"))
	require.False(t, c.called)

	// Without Web Bot Auth the middleware behaves exactly as before.
	r = sign()
	r.Header.Set("Signature-Agent", `sig1="https://buyer.example.com"`)
	c = captured{}
	h = Middleware(MiddlewareOptions{Resolver: resolver, SchemeOverride: "https", Logger: discardLogger()})(captureHandler(&c))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestMiddlewareLogsRFC9421Deprecation(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	_ = Middleware(MiddlewareOptions{Logger: logger})
	require.Equal(t, 1, strings.Count(buf.String(), "deprecated in AdCP 3.3"))
	require.Contains(t, buf.String(), "level=INFO")
	buf.Reset()
	rig := newWBARig(t)
	_ = Middleware(MiddlewareOptions{Logger: logger, WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker}})
	require.NotContains(t, buf.String(), "deprecated")
}

// TestMiddlewareUnboundGateReadsExactKeys: the operation the unbound gate
// sees is the one the MCP SDK dispatches. A case-variant duplicate key
// ("Name" beside "name") must not smuggle a tool past the gate.
func TestMiddlewareUnboundGateReadsExactKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"exact get_principal (positive control)", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_principal","arguments":{}}}`, http.StatusOK},
		{"name and Name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_media_buy","arguments":{},"Name":"get_principal"}}`, http.StatusBadRequest},
		{"method and Method", `{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"initialize","params":{"name":"create_media_buy"}}`, http.StatusBadRequest},
		// Without an exact name the request names no allowed operation, so
		// it is served as anonymous: no identity is attached.
		{"Name only", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"Name":"get_principal","arguments":{}}}`, http.StatusOK},
		{"Method only", `{"jsonrpc":"2.0","id":1,"Method":"tools/call","params":{"name":"get_principal"}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newWBARig(t)
			var c captured
			h := Middleware(MiddlewareOptions{WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, Clock: wbaClock}, Replay: rig.replay, Logger: discardLogger()})(captureHandler(&c))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, rig.signedRequest(t, tc.body))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Equal(t, tc.status == http.StatusOK, c.called)
			if c.called {
				require.Equal(t, strings.HasPrefix(tc.name, "exact"), c.signer != nil, "identity only for the exact allowed operation")
			}
		})
	}
	req := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "https://s.example.com/mcp", strings.NewReader(body))
	}
	require.Equal(t, "get_principal", jsonRPCOperation(req(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_principal"}}`), 0))
	require.Equal(t, "tools/list", jsonRPCOperation(req(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), 0))
	require.Empty(t, jsonRPCOperation(req(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"Name":"get_principal"}}`), 0))
	require.Empty(t, jsonRPCOperation(req(`{"jsonrpc":"1.0","method":"tools/list"}`), 0))
	require.Empty(t, jsonRPCOperation(req(`not json`), 0))
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

// TestMiddlewareSessionMethodsAndTransportRequests: an identity-established
// signer can always open and close an MCP session, whatever UnboundFor
// lists, but a bodyless request that names an operation is still gated.
func TestMiddlewareSessionMethodsAndTransportRequests(t *testing.T) {
	rig := newWBARig(t)
	var c captured
	h := Middleware(MiddlewareOptions{
		WebBotAuth: &WebBotAuthOptions{Keys: rig.resolver, Revocation: rig.checker, Clock: wbaClock, UnboundFor: []string{"get_products"}},
		Replay:     rig.replay, Logger: discardLogger(), OperationResolver: DefaultOperationResolver,
	})(captureHandler(&c))
	serve := func(r *http.Request) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`} {
		require.Equal(t, http.StatusOK, serve(rig.signedRequest(t, body)), body)
	}
	c = captured{}
	require.Equal(t, http.StatusOK, serve(rig.signedRequest(t, mcpCall("get_principal"))))
	require.Nil(t, c.signer, "UnboundFor replaced the default: get_principal is served as anonymous")
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		r := httptest.NewRequest(method, "https://seller.example.com/mcp", nil)
		require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
		require.Equal(t, http.StatusOK, serve(r), method)
	}
	r := httptest.NewRequest(http.MethodGet, "https://seller.example.com/adcp/create_media_buy", nil)
	require.NoError(t, SignWebBotAuthRequest(r, rig.key, WBASignOptions{AgentOrigin: rig.origin, Clock: wbaClock}))
	c = captured{}
	require.Equal(t, http.StatusOK, serve(r))
	require.Nil(t, c.signer, "a resolved operation beyond UnboundFor is served as anonymous, even without a body")
}

// TestMiddlewareCarriesSignerToToolRequests: every verified signer, under
// either profile, is placed in the request's auth.TokenInfo for MCP tool
// handlers, without inventing a UserID or disturbing the Authorization
// header.
func TestMiddlewareCarriesSignerToToolRequests(t *testing.T) {
	res, err := GenerateSigningKey(AlgEd25519, "legacy-key")
	require.NoError(t, err)
	priv, _, _ := LoadPrivateKey(res.PrivateKeyPEM)
	resolver := NewStaticJWKSResolver()
	jwk := res.PublicJWK
	resolver.Put("legacy-key", &jwk, "https://buyer.example.com/mcp")
	legacy, _ := NewSigner(SignerOptions{KeyID: "legacy-key", PrivateKey: priv})
	var ti *auth.TokenInfo
	var authz string
	h := Middleware(MiddlewareOptions{Resolver: resolver, SchemeOverride: "https", Logger: discardLogger()})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ti = auth.TokenInfoFromContext(r.Context())
		authz = r.Header.Get("Authorization")
	}))
	r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/adcp/x", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Basic abc")
	require.NoError(t, legacy.SignRequest(r, SignOptions{CoverContentDigest: true}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.NotNil(t, ti)
	require.Empty(t, ti.UserID)
	s := VerifiedSignerFromTokenInfo(ti)
	require.NotNil(t, s)
	require.Equal(t, "legacy-key", s.KeyID)
	require.Equal(t, "Basic abc", authz, "the original Authorization header is restored")
	require.Nil(t, VerifiedSignerFromToolRequest(nil))
}

// TestUnsignedAmbiguousBodiesNeedASignature: a body the middleware cannot
// read the way the handler will (not strict JSON: duplicate or case-variant
// member names) is treated as needing a signature, so it cannot carry a
// required operation, a required protocol method, or
// push_notification_config.authentication past the checks unsigned.
func TestUnsignedAmbiguousBodiesNeedASignature(t *testing.T) {
	rig := newWBARig(t)
	unsigned := func(body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "https://seller.example.com/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	duplicateID := `{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call","params":{"name":"create_media_buy","arguments":{}}}`
	caseVariantMethod := `{"jsonrpc":"2.0","id":1,"method":"tools/call","Method":"initialize","params":{"name":"get_products"}}`
	casePushAuth := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_media_buys","arguments":{"Push_Notification_Config":{"url":"https://b.example.com/h","Authentication":{"schemes":["Bearer"],"credentials":"x"}}}}}`

	exclusive := func(t *testing.T, opts WebBotAuthOptions, methods []string) (http.Handler, *captured) {
		var c captured
		opts.Keys, opts.Clock, opts.Exclusive = rig.resolver, wbaClock, true
		return Middleware(MiddlewareOptions{
			WebBotAuth: &opts, Replay: rig.replay, Logger: discardLogger(),
			RequiredFor: []string{"create_media_buy"}, ProtocolMethodsRequiredFor: methods,
		})(captureHandler(&c)), &c
	}
	t.Run("Exclusive, no OperationResolver: duplicate keys do not hide a required operation", func(t *testing.T) {
		h, c := exclusive(t, WebBotAuthOptions{}, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(duplicateID))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.False(t, c.called)
	})
	t.Run("Exclusive: a case-variant method does not hide a required protocol method", func(t *testing.T) {
		h, c := exclusive(t, WebBotAuthOptions{}, []string{"tools/call"})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(caseVariantMethod))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.False(t, c.called)
	})
	t.Run("Exclusive: case-variant push_notification_config.authentication needs a signature", func(t *testing.T) {
		h, c := exclusive(t, WebBotAuthOptions{}, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(casePushAuth))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.False(t, c.called)
	})
	t.Run("discovery failure served as anonymous: an ambiguous body is refused", func(t *testing.T) {
		broken := newWBARig(t)
		broken.dirs.fail(broken.origin, errors.New("directory answers 500"))
		var c captured
		h := Middleware(MiddlewareOptions{
			WebBotAuth: &WebBotAuthOptions{Keys: broken.resolver, Clock: wbaClock, OnDiscoveryFailure: DiscoveryFailureAnonymous},
			Replay:     broken.replay, Logger: discardLogger(), RequiredFor: []string{"create_media_buy"},
		})(captureHandler(&c))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, broken.signedRequest(t, duplicateID))
		requireGenericAuthError(t, rec, http.StatusForbidden, true)
		require.False(t, c.called)
		// A strict body naming no required operation is still served as
		// anonymous.
		c = captured{}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, broken.signedRequest(t, mcpCall("get_products")))
		require.Equal(t, http.StatusOK, rec.Code)
		require.True(t, c.called)
		require.Nil(t, c.signer)
	})
	t.Run("RFC 9421 path: unsigned ambiguous or case-variant bodies get request_signature_required", func(t *testing.T) {
		for name, body := range map[string]string{"duplicate id": duplicateID, "case-variant method": caseVariantMethod, "case-variant push auth": casePushAuth} {
			var c captured
			h := Middleware(MiddlewareOptions{Resolver: NewStaticJWKSResolver(), Replay: rig.replay, Logger: discardLogger()})(captureHandler(&c))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, unsigned(body))
			require.Equal(t, http.StatusUnauthorized, rec.Code, name)
			require.Equal(t, `Signature error="request_signature_required"`, rec.Header().Get("WWW-Authenticate"), name)
			require.False(t, c.called, name)
		}
		// A strict body without push authentication still passes unsigned.
		var c captured
		h := Middleware(MiddlewareOptions{Resolver: NewStaticJWKSResolver(), Replay: rig.replay, Logger: discardLogger()})(captureHandler(&c))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, unsigned(mcpCall("get_products")))
		require.Equal(t, http.StatusOK, rec.Code)
	})
	require.Empty(t, protocolMethodFromJSONRPC([]byte(`{"jsonrpc":"2.0","Method":"tools/call"}`)), "method is read by exact name")
	require.Equal(t, "tools/call", protocolMethodFromJSONRPC([]byte(`{"jsonrpc":"2.0","method":"tools/call"}`)))
	require.True(t, bodyCarriesPushNotificationAuth([]byte(casePushAuth)))
}
