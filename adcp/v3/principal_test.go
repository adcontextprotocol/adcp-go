package adcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func staticVerifier(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if token != "good-token" {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: "buyer-42", Expiration: time.Now().Add(time.Hour)}, nil
}

func httpSession(t *testing.T, cfg Config, token string) *mcp.ClientSession {
	t.Helper()
	srv := httptest.NewServer(Handler(func() *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0"}, nil)
		Register(s, cfg)
		return s
	}, WithBearerAuth(staticVerifier)))
	t.Cleanup(srv.Close)
	return connectBearer(t, srv.URL, token)
}

func connectBearer(t *testing.T, baseURL, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestBearerPrincipalScopesIdempotency covers the end-to-end path a seller
// uses: WithBearerAuth supplies the principal that the idempotency store
// requires, replays stay within one buyer, and the same key from another
// buyer executes in that buyer's own scope.
func TestBearerPrincipalScopesIdempotency(t *testing.T) {
	var calls int32
	cfg := baseTestConfig(Config{CreateMediaBuy: countingCreateMediaBuy(&calls)})
	buyers := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if token != "buyer-a" && token != "buyer-b" {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: token, Expiration: time.Now().Add(time.Hour)}, nil
	}
	srv := httptest.NewServer(Handler(func() *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "seller-test", Version: "v0"}, nil)
		Register(s, cfg)
		return s
	}, WithBearerAuth(buyers)))
	t.Cleanup(srv.Close)

	key := idempotency.Generate()
	args := func() map[string]any { return map[string]any{"idempotency_key": key} }

	a := connectBearer(t, srv.URL, "buyer-a")
	first := callSession(t, a, "create_media_buy", args())
	replay := callSession(t, connectBearer(t, srv.URL, "buyer-a"), "create_media_buy", args())
	other := callSession(t, connectBearer(t, srv.URL, "buyer-b"), "create_media_buy", args())

	assert.Equal(t, "mb-1", first["media_buy_id"])
	assert.Equal(t, "mb-1", replay["media_buy_id"], "same buyer replays across sessions")
	assert.Equal(t, true, replay["replayed"])
	assert.Equal(t, "mb-2", other["media_buy_id"], "another buyer's identical key executes in its own scope")
	assert.Nil(t, other["replayed"])
	assert.EqualValues(t, 2, atomic.LoadInt32(&calls))

	anon := callSession(t, connectBearer(t, srv.URL, ""), "create_media_buy", args())
	assert.Equal(t, "SERVICE_UNAVAILABLE", adcpErrorOf(t, anon)["code"], "keyed call without a principal is refused")
	assert.EqualValues(t, 2, atomic.LoadInt32(&calls))
}

func principalEchoConfig(seen *string) Config {
	return baseTestConfig(Config{
		RequirePrincipal: true,
		GetProducts: func(ctx context.Context, _ any, _ *GetProductsRequest) (*ProductsData, error) {
			*seen = PrincipalFromContext(ctx)
			return &ProductsData{Products: []Product{}}, nil
		},
	})
}

func TestBearerPrincipalReachesHandler(t *testing.T) {
	var seen string
	cs := httpSession(t, principalEchoConfig(&seen), "good-token")
	wire := callSession(t, cs, "get_products", map[string]any{"buying_mode": "brief"})
	assert.Nil(t, wire["adcp_error"])
	assert.Equal(t, "buyer-42", seen)
}

func TestRequirePrincipalRejectsAnonymousTools(t *testing.T) {
	var seen string
	cs := httpSession(t, principalEchoConfig(&seen), "")
	e := adcpErrorOf(t, callSession(t, cs, "get_products", map[string]any{"buying_mode": "brief"}))
	assert.Equal(t, "AUTH_REQUIRED", e["code"])
	assert.Empty(t, seen)
}

func TestRequirePrincipalAllowsAnonymousCapabilities(t *testing.T) {
	var seen string
	cs := httpSession(t, principalEchoConfig(&seen), "")
	wire := callSession(t, cs, "get_adcp_capabilities", map[string]any{})
	assert.Nil(t, wire["adcp_error"])
}

func TestBearerAuthRejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(Handler(func() *mcp.Server {
		return mcp.NewServer(&mcp.Implementation{Name: "x", Version: "v0"}, nil)
	}, WithBearerAuth(staticVerifier)))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer bad-token")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestBearerAuthRejectsTokenWithoutPrincipal(t *testing.T) {
	for name, info := range map[string]*auth.TokenInfo{
		"empty UserID": {Expiration: time.Now().Add(time.Hour)},
		"nil info":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			v := func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
				return info, nil
			}
			srv := httptest.NewServer(Handler(func() *mcp.Server {
				return mcp.NewServer(&mcp.Implementation{Name: "x", Version: "v0"}, nil)
			}, WithBearerAuth(v)))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer x")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}

func bearerPost(t *testing.T, h http.Handler, authorization string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", authorization)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func emptyAgent() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "x", Version: "v0"}, nil)
}

func TestBearerAuthRejectsNonBearerScheme(t *testing.T) {
	code, _ := bearerPost(t, Handler(emptyAgent, WithBearerAuth(staticVerifier)), "Basic dXNlcjpwYXNz")
	assert.Equal(t, http.StatusUnauthorized, code, "a present non-Bearer Authorization header is never anonymous")
}

func TestBearerAuthDoesNotLeakVerifierErrors(t *testing.T) {
	v := func(context.Context, string, *http.Request) (*auth.TokenInfo, error) {
		return nil, errors.New("dial tcp 10.0.0.7:5432: connection refused")
	}
	code, body := bearerPost(t, Handler(emptyAgent, WithBearerAuth(v)), "Bearer x")
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.NotContains(t, body, "10.0.0.7")
	assert.NotContains(t, body, "connection refused")
}
