package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
)

var version = "dev"

func main() {
	configFile := flag.String("config", "", "Path to JSON config file")
	addr := flag.String("addr", "", "Listen address (overrides config)")
	flag.Parse()

	// Load config: flags > env vars > JSON config > defaults.
	cfg := loadConfig(*configFile, *addr)

	// Set up structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Initialize components
	registry := router.NewRegistry("", "")
	health := router.NewProviderHealth(cfg.Health.FailureThreshold, time.Duration(cfg.Health.CooldownSeconds)*time.Second)
	r := router.NewRouter(cfg.Providers, registry, nil, health)

	// Metrics
	reg := prommetrics.NewRegistry()
	reg.DefineCounter("router_requests_total", "Total requests by type.", []string{"type"})
	reg.DefineHistogram("router_request_duration_seconds", "Request latency by type.", []string{"type"},
		[]float64{.001, .005, .01, .025, .05, .1, .25, .5, 1})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "context")
		r.HandleContextMatch(w, req)
		reg.HistogramObserve("router_request_duration_seconds", time.Since(start).Seconds(), "context")
		slog.Debug("context match", "request_id", req.Header.Get("X-Request-ID"), "latency_ms", time.Since(start).Milliseconds())
	})
	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "identity")
		r.HandleIdentityMatch(w, req)
		reg.HistogramObserve("router_request_duration_seconds", time.Since(start).Seconds(), "identity")
		slog.Debug("identity match", "request_id", req.Header.Get("X-Request-ID"), "latency_ms", time.Since(start).Milliseconds())
	})
	mux.HandleFunc("GET /registry/snapshot", registry.HandleSnapshot)
	mux.Handle("GET /metrics", reg.Handler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	})

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("shutting down", "signal", sig.String())

		drainTimeout := time.Duration(cfg.Shutdown.DrainSeconds) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
		close(done)
	}()

	slog.Info("TMP Router starting", "addr", cfg.Addr, "providers", len(cfg.Providers), "version", version)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("listen error", "error", err)
		os.Exit(1)
	}
	<-done
	slog.Info("TMP Router stopped")
}

// loadConfig resolves config from flags, env vars, JSON file, and defaults.
// Priority: flags > env vars > JSON config > defaults.
func loadConfig(configFile, addr string) *router.ServerConfig {
	var cfg *router.ServerConfig

	if configFile != "" {
		var err error
		cfg, err = router.LoadServerConfig(configFile)
		if err != nil {
			slog.Error("failed to load config", "path", configFile, "error", err)
			os.Exit(1)
		}
	} else if envConfig := os.Getenv("TMP_ROUTER_CONFIG"); envConfig != "" {
		var err error
		cfg, err = router.LoadServerConfig(envConfig)
		if err != nil {
			slog.Error("failed to load config from TMP_ROUTER_CONFIG", "path", envConfig, "error", err)
			os.Exit(1)
		}
	} else {
		cfg = router.DefaultServerConfig()
	}

	// Env var for addr (flag takes precedence).
	if addr != "" {
		cfg.Addr = addr
	} else if envAddr := os.Getenv("TMP_ROUTER_ADDR"); envAddr != "" {
		cfg.Addr = envAddr
	}

	return cfg
}
