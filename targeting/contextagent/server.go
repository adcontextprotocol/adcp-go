package contextagent

import (
	"encoding/json"
	"log/slog"
	"mime"
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
	Port           int
	ContextHandler http.Handler
	KeyStore       tmproto.KeyStore
	OwnEndpointURL string
	RequireSig     bool
	Registry       *prometheus.Registry
	IsRunning      func() bool
	Version        string
	PprofEnabled   bool

	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int

	// RequestBodyLimit caps the bytes the verifier reads from the
	// signed request before computing the signature. Must match the
	// body limit the inner handler enforces, otherwise a signed
	// request between the verifier limit and the handler limit gets
	// silently truncated and rejected as malformed JSON.
	RequestBodyLimit int64

	AdminPort int

	StrictContentType bool

	// LivenessChecks runs in /live; any check returning a non-nil
	// error flips /live from 200 to 503 with the joined error
	// messages in the response body. Intended for "the agent is up
	// but the data plane is broken" signals (e.g. suppression
	// snapshot is N consecutive refreshes behind). /health stays
	// purely about process readiness — the orchestrator decides
	// which one to probe.
	LivenessChecks []LivenessCheck

	// Recorder observes request lifecycle, panics, and store errors.
	// nil → noop.
	Recorder Recorder

	Logger *slog.Logger
}

// LivenessCheck is one named predicate consulted by /live. Name is
// included in the response when Fn returns an error so an operator can
// see which subsystem is degraded without reading agent logs.
type LivenessCheck struct {
	Name string
	Fn   func() error
}

// NewServer builds the *http.Server for /context and /health. When
// AdminPort == 0 the operator endpoints (/live, /metrics, /debug/pprof)
// also mount on this server's mux. When AdminPort > 0 those are
// omitted here and the caller wires NewAdminServer on a second
// listener.
func NewServer(cfg ServerConfig) *http.Server {
	mux := http.NewServeMux()

	// Middleware order (outermost first): request-metrics records
	// status + latency; strict Content-Type rejects bad CT with 415;
	// signature verification is innermost so a 415 short-circuits
	// before we ever read or sign the body.
	ctxHandler := cfg.ContextHandler
	if cfg.KeyStore != nil {
		ctxHandler = tmproto.VerifyContextMatchHandler(ctxHandler, tmproto.VerifyOptions{
			KeyStore:         cfg.KeyStore,
			OwnEndpointURL:   cfg.OwnEndpointURL,
			RequireSignature: cfg.RequireSig,
			BodyLimit:        cfg.RequestBodyLimit,
		})
	}
	if cfg.StrictContentType {
		ctxHandler = contentTypeJSON(ctxHandler)
	}
	// Middleware order applied (innermost → outermost):
	//   inner verifier → contentTypeJSON → recoverMiddleware → requestMetricsMiddleware
	// Request flow at runtime is the reverse: metrics first (so the
	// deferred RequestCompleted observes the final status), then
	// recover (so a panic in the inner chain becomes a 500 the
	// metrics middleware can see), then the verifier / handler.
	ctxHandler = recoverMiddleware(ctxHandler, cfg.Recorder, cfg.Logger)
	ctxHandler = requestMetricsMiddleware(ctxHandler, cfg.Recorder)
	mux.Handle("POST /context", ctxHandler)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	if cfg.AdminPort == 0 {
		mountAdmin(mux, cfg)
	}

	// One recoverMiddleware on /context (inside ctxHandler) and a
	// second one wrapping the mux. The inner catches /context panics
	// and writes the tmproto.ErrorResponse JSON shape callers expect;
	// the outer is the safety net for /health and the operator
	// endpoints (/live, /metrics, /debug/pprof) when AdminPort == 0,
	// which the inner never sees. A panic in /health is a contract
	// violation either way (the spec defines only "ok" / "not ready"
	// bodies); writing a generic JSON 500 beats the default net/http
	// stack trace.
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

// AdminServerConfig packages the inputs for NewAdminServer.
type AdminServerConfig struct {
	Port              int
	Registry          *prometheus.Registry
	Version           string
	IsRunning         func() bool
	PprofEnabled      bool
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	LivenessChecks    []LivenessCheck
	Recorder          Recorder
	Logger            *slog.Logger
}

// NewAdminServer builds the observability mux (/live, /metrics,
// /debug/pprof) on its own listener.
func NewAdminServer(cfg AdminServerConfig) *http.Server {
	mux := http.NewServeMux()
	mountAdmin(mux, ServerConfig{
		Registry:       cfg.Registry,
		Version:        cfg.Version,
		IsRunning:      cfg.IsRunning,
		PprofEnabled:   cfg.PprofEnabled,
		LivenessChecks: cfg.LivenessChecks,
	})
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

func mountAdmin(mux *http.ServeMux, cfg ServerConfig) {
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) {
		// Drain order: shutting-down beats degraded-liveness because
		// the orchestrator should stop sending traffic before it
		// considers recycling the pod.
		if cfg.IsRunning != nil && !cfg.IsRunning() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "shutting_down", "version": cfg.Version,
			})
			return
		}
		degraded := map[string]string{}
		for _, check := range cfg.LivenessChecks {
			if check.Fn == nil {
				continue
			}
			if err := check.Fn(); err != nil {
				degraded[check.Name] = err.Error()
			}
		}
		if len(degraded) > 0 {
			body := map[string]any{
				"status":   "degraded",
				"version":  cfg.Version,
				"failures": degraded,
			}
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": cfg.Version})
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
			// RFC 7231 §3.1.1.1 makes media types case-insensitive.
			// mime.ParseMediaType lowercases the type/subtype while
			// trimming the charset / boundary parameters this check
			// doesn't care about.
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
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
