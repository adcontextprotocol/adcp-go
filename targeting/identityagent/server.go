package identityagent

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ServerConfig packages the inputs for NewServer. All four HTTP timeouts
// are required (Config.Validate rejects zero values) and act as outer
// listener bounds — the per-request 40ms budget is enforced inside the
// identity handler via context.WithTimeout.
//
// /identity and /health always stay on Port — both are part of the TMP
// protocol surface that publisher routers probe externally, so they MUST
// share the listener exposed at the registered base URL. When AdminPort > 0
// the operator-facing endpoints (/live, /metrics, /debug/pprof) move onto
// a second listener built by NewAdminServer.
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
	MaxHeaderBytes    int

	// AdminPort decides whether observability endpoints share the main
	// listener (=0) or split onto a second listener built by NewAdminServer
	// (>0). Callers that get an AdminPort > 0 must also instantiate the
	// admin server themselves; NewServer only signals whether to mount the
	// observability endpoints on the main mux.
	AdminPort int

	// Middleware knobs for the main mux. Match Config flags 1:1.
	StrictContentType bool
	AccessLogEnabled  bool

	Recorder Recorder
	Logger   *slog.Logger
}

// NewServer builds the *http.Server for /identity and /health. When
// AdminPort == 0 the operator endpoints (/live, /metrics, /debug/pprof)
// also mount on this server's mux. When AdminPort > 0 those are omitted
// here and the caller wires NewAdminServer onto a second listener; /health
// stays on the main mux unconditionally because it's part of the TMP
// protocol surface that publisher routers probe externally.
//
// The handler chain on POST /identity reads outermost-to-innermost:
//
//	otelhttp.NewHandler                # extract inbound traceparent
//	→ recoverMiddleware                # trap panics, record + log + 500
//	  → requestIDMiddleware            # echo X-Request-ID
//	    → accessLogMiddleware          # one structured line per request
//	      → contentTypeMiddleware      # 415 unless application/json
//	        → tmproto.VerifyIdentityMatchHandler  # TMP signature
//	          → identityHandler        # body decode + Service.Evaluate
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
	identity = contentTypeMiddleware(identity, cfg.StrictContentType)
	identity = accessLogMiddleware(identity, cfg.AccessLogEnabled, cfg.Logger)
	identity = requestIDMiddleware(identity)
	identity = recoverMiddleware(identity, cfg.Recorder, cfg.Logger)
	identity = otelhttp.NewHandler(identity, "POST /identity")
	mux.Handle("POST /identity", identity)

	mountHealthEndpoint(mux, cfg.IsRunning)
	if cfg.AdminPort == 0 {
		mountOperatorEndpoints(mux, cfg.Registry, cfg.Version, cfg.PprofEnabled)
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

// AdminServerConfig packages the inputs for NewAdminServer. Only used when
// Config.AdminPort > 0.
type AdminServerConfig struct {
	Port         int
	Registry     *prometheus.Registry
	Version      string
	PprofEnabled bool

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	Recorder Recorder
	Logger   *slog.Logger
}

// NewAdminServer builds the *http.Server hosting /metrics, /live, and
// (when enabled) /debug/pprof on a separate port. /health stays on the
// main server (see NewServer) — it's part of the protocol surface and
// must share the listener publisher routers reach externally. The mux
// is wrapped in recoverMiddleware so a panic in any observability handler
// doesn't take the process down with it.
func NewAdminServer(cfg AdminServerConfig) *http.Server {
	mux := http.NewServeMux()
	mountOperatorEndpoints(mux, cfg.Registry, cfg.Version, cfg.PprofEnabled)

	return &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Port),
		Handler:           recoverMiddleware(mux, cfg.Recorder, cfg.Logger),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}

// mountHealthEndpoint registers GET /health on the supplied mux. The
// response body is constrained by the TMP spec: it MUST be exactly
// {"status":"ok"} when ready (200) or {"status":"not ready"} when not
// (503), with no version, build hash, hostname, or subsystem detail.
// Differentiating bodies or status codes by failing subsystem would be
// a side channel mapping external probes onto internal topology.
func mountHealthEndpoint(mux *http.ServeMux, isRunning func() bool) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if isRunning != nil && isRunning() {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not ready"}`))
	})
}

// mountOperatorEndpoints registers /live, /metrics (when Registry is
// non-nil), and pprof endpoints (when enabled) on the supplied mux.
// Shared by NewServer (when AdminPort == 0) and NewAdminServer.
func mountOperatorEndpoints(mux *http.ServeMux, reg *prometheus.Registry, version string, pprofEnabled bool) {
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
	})

	if reg != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	}

	if pprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
}

// runningFlag is a small wrapper around atomic.Bool that exposes a
// closure-friendly Load method for the /health endpoint.
type runningFlag struct{ b atomic.Bool }

func (r *runningFlag) Store(v bool) { r.b.Store(v) }
func (r *runningFlag) Load() bool   { return r.b.Load() }
