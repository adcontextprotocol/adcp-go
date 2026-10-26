package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/require"
)

// TestAdapterRoundTrip runs the four operations against each other: the
// adapter's own signed directory and request verify, and replaying the
// request through prior is refused with 429.
func TestAdapterRoundTrip(t *testing.T) {
	f := newFixture(t)

	got := f.run(t, operation{Op: "verify_directory", Origin: origin, Response: f.dir, Now: f.now})
	require.Equal(t, true, got["ok"], got)
	keyids := got["keyids"].([]string)
	require.Len(t, keyids, 1)

	verify := operation{Op: "verify_request", Request: f.signed, Directories: map[string]message{origin: f.dir}, Now: f.now}
	got = f.run(t, verify)
	require.Equal(t, true, got["ok"], got)
	require.Equal(t, origin, got["identity"])
	require.Equal(t, keyids[0], got["keyid"])

	verify.Prior = []message{f.signed}
	got = f.run(t, verify)
	require.Equal(t, false, got["ok"])
	require.Equal(t, http.StatusTooManyRequests, got["status"])
}

// TestAdapterRefusesOnlyForSDKRefusals: an SDK verification refusal is
// {ok: false}; anything else is an adapter failure, which main turns into a
// crash, so the harness never grades it as a refusal.
func TestAdapterRefusesOnlyForSDKRefusals(t *testing.T) {
	f := newFixture(t)
	tampered := f.signed
	tampered.Body = `{"jsonrpc":"2.0","id":2}`

	got := f.run(t, operation{Op: "verify_request", Request: tampered, Directories: map[string]message{origin: f.dir}, Now: f.now})
	require.Equal(t, false, got["ok"])
	require.Equal(t, http.StatusForbidden, got["status"])
	require.Equal(t, "signature", got["rule"])

	unsigned := f.dir
	unsigned.Headers = map[string]string{"content-type": f.dir.Headers["content-type"]}
	got = f.run(t, operation{Op: "verify_directory", Origin: origin, Response: unsigned, Now: f.now})
	require.Equal(t, false, got["ok"])
	require.Equal(t, "directory", got["rule"])

	badMethod := f.signed
	badMethod.Method = "GE T"
	for name, op := range map[string]operation{
		"unknown operation":         {Op: "unknown"},
		"prior request fails":       {Op: "verify_request", Request: f.signed, Prior: []message{tampered}, Directories: map[string]message{origin: f.dir}, Now: f.now},
		"request cannot be built":   {Op: "verify_request", Request: badMethod, Directories: map[string]message{origin: f.dir}, Now: f.now},
		"signer refuses the origin": {Op: "sign_request", Key: f.key, Origin: "http://buyer.example.com", Now: f.now, Request: message{Method: http.MethodGet, URL: "https://seller.example.com/"}},
		"directory signer refuses":  {Op: "sign_directory", Keys: []json.RawMessage{f.key}, Origin: "http://buyer.example.com", Now: f.now},
		"key is not a private JWK":  {Op: "sign_request", Key: json.RawMessage(`{"kty":"OKP","crv":"Ed25519","x":"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}`), Origin: origin, Now: f.now},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := run(op)
			require.Error(t, err)
			require.Nil(t, out)
		})
	}
	_, err := runInput(strings.NewReader("{not json"))
	require.Error(t, err)
}

const origin = "https://buyer.example.com"

type fixture struct {
	key    json.RawMessage
	now    int64
	dir    message
	signed message
}

func newFixture(t *testing.T) fixture {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := json.Marshal(jose.JSONWebKey{Key: priv})
	require.NoError(t, err)
	f := fixture{key: key, now: time.Now().Unix()}
	out, err := run(operation{Op: "sign_directory", Keys: []json.RawMessage{key}, Origin: origin, Now: f.now})
	require.NoError(t, err)
	f.dir = out.(message)
	out, err = run(operation{Op: "sign_request", Key: key, Origin: origin, Now: f.now, Request: message{
		Method: http.MethodPost, URL: "https://seller.example.com/mcp/",
		Headers: map[string]string{"content-type": "application/json"}, Body: `{"jsonrpc":"2.0","id":1}`,
	}})
	require.NoError(t, err)
	f.signed = out.(message)
	return f
}

func (fixture) run(t *testing.T, op operation) map[string]any {
	t.Helper()
	out, err := run(op)
	require.NoError(t, err)
	return out.(map[string]any)
}
