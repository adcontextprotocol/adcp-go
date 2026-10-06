package adcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: token}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
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
	defer resp.Body.Close()
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
			defer resp.Body.Close()
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		})
	}
}
