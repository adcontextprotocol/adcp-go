package identityagent

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ServerConfig packages the inputs for NewServer. All four HTTP timeouts
// are required (Config.Validate rejects zero values) and act as outer
// listener bounds — the per-request 40ms budget is enforced inside the
// identity handler via context.WithTimeout.
type ServerConfig struct {
	Port            int
	IdentityHandler http.Handler
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
}

// NewServer builds the *http.Server that exposes /tmp/identity, /live,
// /health, and (when configured) /metrics + /debug/pprof.
//
// /live returns 200 while the process is alive — never gated by IsRunning so
// kubelet keeps the pod attached during graceful shutdown.
//
// /health returns 200 while IsRunning() is true and 503 after the agent
// flips it false at shutdown start — k8s drains pods from the Service
// endpoints before /tmp/identity stops responding.
func NewServer(cfg ServerConfig) *http.Server {
	mux := http.NewServeMux()

	identity := cfg.IdentityHandler
	if cfg.KeyStore != nil {
		identity = tmproto.VerifyIdentityMatchHandler(identity, tmproto.VerifyOptions{
			KeyStore:         cfg.KeyStore,
			OwnEndpointURL:   cfg.OwnEndpointURL,
			RequireSignature: cfg.RequireSig,
		})
	}
	mux.Handle("POST /tmp/identity", identity)

	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, cfg.Version)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if cfg.IsRunning != nil && cfg.IsRunning() {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, cfg.Version)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
	})

	if cfg.Registry != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(cfg.Registry, promhttp.HandlerOpts{Registry: cfg.Registry}))
	}

	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// runningFlag is a small wrapper around atomic.Bool that exposes a
// closure-friendly Load method for the /health endpoint.
type runningFlag struct{ b atomic.Bool }

func (r *runningFlag) Store(v bool) { r.b.Store(v) }
func (r *runningFlag) Load() bool   { return r.b.Load() }
