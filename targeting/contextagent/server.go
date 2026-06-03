package contextagent

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ServerConfig packages the inputs for NewServer.
type ServerConfig struct {
	Port            int
	ContextHandler  http.Handler
	KeyStore        tmproto.KeyStore
	OwnEndpointURL  string
	RequireSig      bool
	Registry        *prometheus.Registry
	IsRunning       func() bool
	Version         string
	PprofEnabled    bool

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	AdminPort int

	StrictContentType bool

	Logger *slog.Logger
}

// NewServer builds the *http.Server for /context and /health. When
// AdminPort == 0 the operator endpoints (/live, /metrics, /debug/pprof)
// also mount on this server's mux. When AdminPort > 0 those are
// omitted here and the caller wires NewAdminServer on a second
// listener.
func NewServer(cfg ServerConfig) *http.Server {
	mux := http.NewServeMux()

	ctxHandler := cfg.ContextHandler
	if cfg.StrictContentType {
		ctxHandler = contentTypeJSON(ctxHandler)
	}
	if cfg.KeyStore != nil {
		ctxHandler = tmproto.VerifyContextMatchHandler(ctxHandler, tmproto.VerifyOptions{
			KeyStore:         cfg.KeyStore,
			OwnEndpointURL:   cfg.OwnEndpointURL,
			RequireSignature: cfg.RequireSig,
		})
	}
	mux.Handle("POST /context", ctxHandler)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	if cfg.AdminPort == 0 {
		mountAdmin(mux, cfg)
	}

	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}

// AdminServerConfig packages the inputs for NewAdminServer.
type AdminServerConfig struct {
	Port           int
	Registry       *prometheus.Registry
	Version        string
	IsRunning      func() bool
	PprofEnabled   bool
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	Logger            *slog.Logger
}

// NewAdminServer builds the observability mux (/live, /metrics,
// /debug/pprof) on its own listener.
func NewAdminServer(cfg AdminServerConfig) *http.Server {
	mux := http.NewServeMux()
	mountAdmin(mux, ServerConfig{
		Registry:     cfg.Registry,
		Version:      cfg.Version,
		IsRunning:    cfg.IsRunning,
		PprofEnabled: cfg.PprofEnabled,
	})
	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}

func mountAdmin(mux *http.ServeMux, cfg ServerConfig) {
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		status := "ok"
		code := http.StatusOK
		if cfg.IsRunning != nil && !cfg.IsRunning() {
			status = "shutting_down"
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, map[string]string{"status": status, "version": cfg.Version})
	})
	if cfg.Registry != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(cfg.Registry, promhttp.HandlerOpts{}))
	}
	if cfg.PprofEnabled {
		mux.HandleFunc("GET /debug/pprof/", pprof.Index)
		mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	}
}

func contentTypeJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			ct := r.Header.Get("Content-Type")
			// Strict mode rejects empty AND wrong Content-Type;
			// otherwise a client that omits the header would slip
			// past the type check entirely. Allow charset suffixes
			// ("application/json; charset=utf-8").
			if len(ct) < len("application/json") || ct[:len("application/json")] != "application/json" {
				writeError(w, "", tmproto.ErrorCodeInvalidRequest, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
