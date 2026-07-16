package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adcontextprotocol/adcp-go/router"
	"github.com/adcontextprotocol/adcp-go/targeting/prommetrics"
	"github.com/adcontextprotocol/adcp-go/tmproto"
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
	fanOutMetrics := &fanOutMetricsAdapter{} // set after metrics registry is created

	signer, signerErr := loadSigner(&cfg.Signing)
	if signerErr != nil {
		slog.Error("invalid signing configuration", "error", signerErr)
		os.Exit(1)
	}
	if signer != nil {
		jwk := signer.PublicJWK()
		// Seed the registry with property records the operator authorized us
		// to sign for, so providers fetching /registry/snapshot pick up the
		// public key alongside the property metadata.
		seedSigningProperties(registry, cfg.Signing.PropertyRIDs, jwk)
	}

	routerOpts := []router.RouterOption{
		router.WithLatencyBudget(cfg.LatencyBudget()),
		router.WithFanOutMetrics(fanOutMetrics),
	}
	if signer != nil {
		routerOpts = append(routerOpts, router.WithTMPSigner(signer))
	}
	r, err := router.NewRouter(cfg.Providers, registry, health, routerOpts...)
	if err != nil {
		slog.Error("invalid router configuration", "error", err)
		os.Exit(1)
	}

	// Metrics
	reg := prommetrics.NewRegistry()
	reg.DefineCounter("router_requests_total", "Total requests by type.", []string{"type"})
	reg.DefineHistogram("router_request_duration_seconds", "Request latency by type.", []string{"type"},
		[]float64{.001, .005, .01, .025, .05, .1, .25, .5, 1})
	reg.DefineGauge("tmp_provider_health_status", "Provider health status (1=healthy, 0=unhealthy).", []string{"provider"})
	reg.DefineHistogram("tmp_provider_health_check_duration_ms", "Health check latency.", []string{"provider"},
		[]float64{1, 5, 10, 25, 50, 100, 500})
	reg.DefineCounter("tmp_provider_excluded_total", "Times a provider was excluded from fan-out.", []string{"provider"})
	reg.DefineCounter("tmp_provider_recovered_total", "Times a provider recovered from exclusion.", []string{"provider"})

	// Metric series documented in docs/trusted-match/router-architecture.mdx
	// §Monitoring. Operator dashboards and alerts derive from these names —
	// keep them aligned with the spec table.
	matchDurationBuckets := []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000}
	reg.DefineHistogram("tmp_context_match_duration_ms", "Context Match end-to-end latency.", nil, matchDurationBuckets)
	reg.DefineHistogram("tmp_identity_match_duration_ms", "Identity Match end-to-end latency.", nil, matchDurationBuckets)
	reg.DefineHistogram("tmp_provider_duration_ms", "Per-provider response time.", []string{"provider"}, matchDurationBuckets)
	reg.DefineCounter("tmp_provider_timeout_total", "Per-provider call timeouts.", []string{"provider"})
	reg.DefineCounter("tmp_provider_error_total", "Per-provider non-timeout errors.", []string{"provider"})
	reg.DefineCounter("tmp_offers_total", "Total offers returned across all providers.", nil)

	// Wire fan-out metrics now that registry exists.
	fanOutMetrics.reg = reg

	// Health checker
	hcMetrics := &healthCheckMetricsAdapter{reg: reg}
	hc := router.NewHealthChecker(r.Providers(), health, cfg.HealthCheck,
		router.WithHealthCheckMetrics(hcMetrics))
	hc.Preflight(context.Background())
	hc.Start()

	// Optional dynamic discovery
	var disc *router.Discovery
	if cfg.Discovery.Endpoint != "" {
		disc = router.NewDiscovery(r.Providers(), health, cfg.Discovery, cfg.LatencyBudget())
		disc.Start()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tmp/context", func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "context")
		r.HandleContextMatch(w, req)
		elapsed := time.Since(start)
		reg.HistogramObserve("router_request_duration_seconds", elapsed.Seconds(), "context")
		reg.HistogramObserve("tmp_context_match_duration_ms", float64(elapsed.Milliseconds()))
		slog.Debug("context match", "request_id", req.Header.Get("X-Request-ID"), "latency_ms", elapsed.Milliseconds())
	})
	mux.HandleFunc("POST /tmp/identity", func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "identity")
		r.HandleIdentityMatch(w, req)
		elapsed := time.Since(start)
		reg.HistogramObserve("router_request_duration_seconds", elapsed.Seconds(), "identity")
		reg.HistogramObserve("tmp_identity_match_duration_ms", float64(elapsed.Milliseconds()))
		slog.Debug("identity match", "request_id", req.Header.Get("X-Request-ID"), "latency_ms", elapsed.Milliseconds())
	})
	mux.HandleFunc("GET /registry/snapshot", registry.HandleSnapshot)
	mux.Handle("GET /metrics", reg.Handler())
	// /healthz is the route the spec advertises; /health is kept for back-compat
	// with existing probes that pre-date the spec wording.
	healthHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	}
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /providers", func(w http.ResponseWriter, _ *http.Request) {
		type providerInfo struct {
			router.ProviderConfig
			Health router.ProviderStatsSnapshot `json:"health"`
		}
		snap := health.Snapshot()
		providers := r.Providers().All()
		out := make([]providerInfo, len(providers))
		for i, p := range providers {
			out[i] = providerInfo{ProviderConfig: p, Health: snap[p.ID]}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
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

		if disc != nil {
			disc.Stop()
		}
		hc.Stop()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
		close(done)
	}()

	slog.Info("TMP Router starting",
		"addr", cfg.Addr,
		"providers", len(cfg.Providers),
		"tls", cfg.TLS.Enabled(),
		"version", version,
	)
	serveErr := listenAndServe(srv, cfg.TLS)
	if serveErr != nil && serveErr != http.ErrServerClosed {
		slog.Error("listen error", "error", serveErr)
		os.Exit(1)
	}
	<-done
	slog.Info("TMP Router stopped")
}

// listenAndServe picks HTTPS when both cert and key are configured, HTTP
// otherwise. Deployments that terminate TLS at an upstream ingress leave the
// TLS block empty and this falls through to ListenAndServe.
func listenAndServe(srv *http.Server, tlsCfg router.TLSConfig) error {
	if tlsCfg.Enabled() {
		return srv.ListenAndServeTLS(tlsCfg.CertPath, tlsCfg.KeyPath)
	}
	return srv.ListenAndServe()
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

	// Signing config — env vars override JSON, flags take precedence above
	// neither (the router has no signing flags today).
	if v := os.Getenv("TMP_ROUTER_SIGNING_KID"); v != "" {
		cfg.Signing.KeyID = v
	}
	if v := os.Getenv("TMP_ROUTER_SIGNING_KEY_PATH"); v != "" {
		cfg.Signing.PrivateKeyPath = v
	}
	if v := os.Getenv("TMP_ROUTER_SIGNING_PROPERTY_RIDS"); v != "" {
		cfg.Signing.PropertyRIDs = splitAndTrim(v)
	}
	if v := os.Getenv("TMP_ROUTER_SIGNING_DISABLED"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Signing.Disabled = true
	}

	// TLS config — env vars override JSON; both cert and key must be set to
	// enable HTTPS. Leaving both unset serves cleartext HTTP (typical when
	// TLS is terminated by an upstream ingress).
	if v := os.Getenv("TMP_ROUTER_TLS_CERT"); v != "" {
		cfg.TLS.CertPath = v
	}
	if v := os.Getenv("TMP_ROUTER_TLS_KEY"); v != "" {
		cfg.TLS.KeyPath = v
	}

	if err := cfg.TLS.Validate(); err != nil {
		slog.Error("invalid TLS configuration", "error", err)
		os.Exit(1)
	}

	return cfg
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadSigner builds a tmproto.Signer from the signing config, fail-closed when
// the operator has not provided a key and has not explicitly opted out.
func loadSigner(cfg *router.SigningConfig) (*tmproto.Signer, error) {
	if cfg.Disabled {
		slog.Warn("TMP request signing is disabled — fan-outs to spec-conformant providers will be rejected", "set_to_enable", "TMP_ROUTER_SIGNING_KEY_PATH")
		return nil, nil
	}
	if cfg.KeyID == "" || cfg.PrivateKeyPath == "" {
		return nil, errors.New("signing.key_id and signing.private_key_path are required (or set signing.disabled=true / TMP_ROUTER_SIGNING_DISABLED=true to opt out)")
	}
	pemBytes, err := os.ReadFile(cfg.PrivateKeyPath) //nolint:gosec // path is from operator config
	if err != nil {
		return nil, fmt.Errorf("read signing key %q: %w", cfg.PrivateKeyPath, err)
	}
	priv, err := tmproto.LoadEd25519PrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing key %q: %w", cfg.PrivateKeyPath, err)
	}
	signer, err := tmproto.NewSigner(cfg.KeyID, priv)
	if err != nil {
		return nil, err
	}
	slog.Info("TMP signer loaded", "kid", cfg.KeyID, "properties", len(cfg.PropertyRIDs))
	return signer, nil
}

// seedSigningProperties ensures every authorized property RID has a record in
// the registry with the router's public key attached. Records that don't exist
// yet (typical when running without a registry sync source) are created with
// just the RID + signing key so downstream providers can resolve the kid.
func seedSigningProperties(registry *router.Registry, propertyRIDs []string, jwk tmproto.SigningKey) {
	if len(propertyRIDs) == 0 {
		return
	}
	for _, rid := range propertyRIDs {
		if _, ok := registry.LookupByRID(rid); !ok {
			registry.ApplyUpdate(&router.RegistryUpdate{
				Sequence: registry.Sequence() + 1,
				Action:   "add",
				Property: router.RegistryProperty{
					PropertyRID: rid,
					PropertyID:  rid, // placeholder until registry sync provides a slug
				},
			})
		}
		if !registry.AttachSigningKey(rid, jwk) {
			slog.Warn("could not attach signing key to property", "property_rid", rid)
		}
	}
}

// healthCheckMetricsAdapter bridges router.HealthCheckMetrics to prommetrics.
type healthCheckMetricsAdapter struct {
	reg *prommetrics.Registry
}

func (a *healthCheckMetricsAdapter) SetHealthStatus(providerID string, healthy bool) {
	v := float64(0)
	if healthy {
		v = 1
	}
	a.reg.GaugeSet("tmp_provider_health_status", v, providerID)
}

func (a *healthCheckMetricsAdapter) ObserveCheckDuration(providerID string, ms float64) {
	a.reg.HistogramObserve("tmp_provider_health_check_duration_ms", ms, providerID)
}

func (a *healthCheckMetricsAdapter) IncExcluded(providerID string) {
	a.reg.CounterInc("tmp_provider_excluded_total", providerID)
}

func (a *healthCheckMetricsAdapter) IncRecovered(providerID string) {
	a.reg.CounterInc("tmp_provider_recovered_total", providerID)
}

// fanOutMetricsAdapter bridges router.FanOutMetrics to prommetrics.
// Series names follow the table in docs/trusted-match/router-architecture.mdx
// §Monitoring.
type fanOutMetricsAdapter struct {
	reg *prommetrics.Registry
}

func (a *fanOutMetricsAdapter) IncExcluded(providerID string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_provider_excluded_total", providerID)
	}
}

func (a *fanOutMetricsAdapter) ObserveProviderDuration(providerID string, ms float64) {
	if a.reg != nil {
		a.reg.HistogramObserve("tmp_provider_duration_ms", ms, providerID)
	}
}

func (a *fanOutMetricsAdapter) IncProviderTimeout(providerID string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_provider_timeout_total", providerID)
	}
}

func (a *fanOutMetricsAdapter) IncProviderError(providerID string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_provider_error_total", providerID)
	}
}

func (a *fanOutMetricsAdapter) AddOffers(n int) {
	if a.reg != nil {
		a.reg.CounterAdd("tmp_offers_total", int64(n))
	}
}
