package adcp

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeOption configures the HTTP server.
type ServeOption func(*serveConfig)

type serveConfig struct {
	port int
	path string
}

// WithPort sets the listen port (default: PORT env or 3001).
func WithPort(port int) ServeOption {
	return func(c *serveConfig) { c.port = port }
}

// WithPath sets the MCP endpoint path (default: /mcp).
func WithPath(path string) ServeOption {
	return func(c *serveConfig) { c.path = path }
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

	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return createAgent()
	}, nil)

	mux := http.NewServeMux()
	mux.Handle(cfg.path, handler)
	mux.Handle(cfg.path+"/", handler)

	addr := fmt.Sprintf(":%d", cfg.port)
	url := fmt.Sprintf("http://localhost:%d%s", cfg.port, cfg.path)

	log.Printf("AdCP agent running at %s", url)
	log.Printf("\nTest with:\n  npx @adcp/client %s", url)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}
