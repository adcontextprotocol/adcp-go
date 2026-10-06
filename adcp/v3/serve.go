package adcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeOption configures the HTTP server.
type ServeOption func(*serveConfig)

type serveConfig struct {
	port     int
	path     string
	verifier auth.TokenVerifier
}

// WithPort sets the listen port (default: PORT env or 3001).
func WithPort(port int) ServeOption {
	return func(c *serveConfig) { c.port = port }
}

// WithPath sets the MCP endpoint path (default: /mcp).
func WithPath(path string) ServeOption {
	return func(c *serveConfig) { c.path = path }
}

// WithBearerAuth verifies Authorization: Bearer tokens with verifier. The
// verifier must set TokenInfo.UserID (the principal) to a non-empty value
// (otherwise the request gets 401) and a non-zero
// Expiration (go-sdk rejects tokens without one; use a far-future time for
// static API keys). Requests without an Authorization header pass through
// anonymously so get_adcp_capabilities stays discoverable; a present
// Authorization header that is not a valid Bearer token is rejected with 401,
// never treated as anonymous. Combine with
// Config.RequirePrincipal to reject anonymous calls to other tools.
func WithBearerAuth(verifier auth.TokenVerifier) ServeOption {
	return func(c *serveConfig) { c.verifier = verifier }
}

// Handler returns the AdCP MCP HTTP handler Serve uses, for mounting in your
// own http.Server or router.
func Handler(createAgent func() *mcp.Server, opts ...ServeOption) http.Handler {
	cfg := &serveConfig{path: "/mcp"}
	for _, opt := range opts {
		opt(cfg)
	}
	var h http.Handler = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return createAgent()
	}, nil)
	if cfg.verifier != nil {
		h = optionalBearer(cfg.verifier, h)
	}
	mux := http.NewServeMux()
	mux.Handle(cfg.path, h)
	mux.Handle(cfg.path+"/", h)
	return mux
}

func optionalBearer(verifier auth.TokenVerifier, next http.Handler) http.Handler {
	// A verified token must name a principal: idempotency keys and account
	// checks are scoped by it, so a token without one is rejected as invalid.
	checked := func(ctx context.Context, token string, r *http.Request) (*auth.TokenInfo, error) {
		info, err := verifier(ctx, token, r)
		if err != nil {
			return nil, err
		}
		if info == nil || info.UserID == "" {
			return nil, fmt.Errorf("%w: token has no usable principal (UserID)", auth.ErrInvalidToken)
		}
		return info, nil
	}
	required := auth.RequireBearerToken(checked, nil)(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			next.ServeHTTP(w, r)
			return
		}
		required.ServeHTTP(w, r)
	})
}

// Serve starts an HTTP server that serves an AdCP MCP agent.
//
// createAgent is called to get the MCP server instance. The handler
// uses StreamableHTTPHandler from the Go MCP SDK.
//
//	server := mcp.NewServer(...)
//	// add tools...
//	adcp.Serve(func() *mcp.Server { return server })
func Serve(createAgent func() *mcp.Server, opts ...ServeOption) error {
	cfg := &serveConfig{path: "/mcp"}

	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.port == 0 {
		if p := os.Getenv("PORT"); p != "" {
			if n, err := strconv.Atoi(p); err == nil {
				cfg.port = n
			}
		}
		if cfg.port == 0 {
			cfg.port = 3001
		}
	}

	addr := fmt.Sprintf(":%d", cfg.port)
	url := fmt.Sprintf("http://localhost:%d%s", cfg.port, cfg.path)

	log.Printf("AdCP agent running at %s", url)
	log.Printf("\nTest with:\n  npx @adcp/client %s", url)

	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(createAgent, opts...),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
