package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configFile := flag.String("config", "", "Path to JSON config file")
	addr := flag.String("addr", "", "Listen address (overrides config)")
	flag.Parse()

	// Load config
	var cfg *ServerConfig
	if *configFile != "" {
		var err error
		cfg, err = LoadServerConfig(*configFile)
		if err != nil {
			slog.Error("failed to load config", "path", *configFile, "error", err)
			os.Exit(1)
		}
	} else {
		cfg = DefaultServerConfig()
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	// Set up structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Initialize components
	registry := NewRegistry("", "")
	health := NewProviderHealth(cfg.Health.FailureThreshold, time.Duration(cfg.Health.CooldownSeconds)*time.Second)
	router := NewRouter(cfg.Providers, registry, nil, health)
	metrics := NewMetrics(health, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, r *http.Request) {
		metrics.ContextRequests.Add(1)
		router.HandleContextMatch(w, r)
	})
	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, r *http.Request) {
		metrics.IdentityRequests.Add(1)
		router.HandleIdentityMatch(w, r)
	})
	mux.HandleFunc("GET /registry/snapshot", registry.HandleSnapshot)
	mux.HandleFunc("GET /metrics", metrics.HandleMetrics)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
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

	slog.Info("TMP Router starting", "addr", cfg.Addr, "providers", len(cfg.Providers))
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("listen error", "error", err)
		os.Exit(1)
	}
	<-done
	slog.Info("TMP Router stopped")
}
