package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	var routerKey tmproto.SigningKey
	if signer != nil {
		routerKey = signer.PublicJWK()
	}

	// Optional AdCP registry feed sync. When configured, replaces the
	// stub /registry/snapshot with live property metadata from the feed.
	// When enabled, the bridge is the sole publisher into router.Registry:
	// each successful poll rebuilds the whole snapshot (feed properties
	// merged with the router's signing key on authorized RIDs) in one
	// atomic LoadFromData call. When disabled, seedSigningProperties is
	// called once at startup so the seed-only stub mode still serves the
	// authorized-RID records.
	bridge, bridgeErr := buildRegistryBridge(cfg.Registry, registry, cfg.Signing.PropertyRIDs, routerKey, signer != nil, slog.Default())
	if bridgeErr != nil {
		slog.Error("registry feed bootstrap failed", "error", bridgeErr)
		os.Exit(1)
	}
	if bridge == nil && signer != nil {
		seedSigningProperties(registry, cfg.Signing.PropertyRIDs, routerKey)
	}

	cacheMetrics := &contextCacheMetricsAdapter{}
	var contextCache *router.ContextCache
	if !cfg.Cache.Disabled {
		contextCache = router.NewContextCache(
			cfg.Cache.DefaultTTL(),
			router.WithContextCacheMetrics(cacheMetrics),
			router.WithContextCacheMaxEntries(cfg.Cache.MaxEntriesResolved()),
		)
	}

	routerOpts := []router.RouterOption{
		router.WithLatencyBudget(cfg.LatencyBudget()),
		router.WithFanOutMetrics(fanOutMetrics),
	}
	if signer != nil {
		routerOpts = append(routerOpts, router.WithTMPSigner(signer))
	}
	if contextCache != nil {
		routerOpts = append(routerOpts, router.WithContextCache(contextCache))
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
	reg.DefineCounter("tmp_context_cache_hits_total", "Context Match cache hits by provider.", []string{"provider"})
	reg.DefineCounter("tmp_context_cache_misses_total", "Context Match cache misses by provider.", []string{"provider"})
	reg.DefineCounter("tmp_router_auth_rejected_total", "Inbound publisher requests rejected by authentication.", []string{"reason"})

	// Wire fan-out metrics now that registry exists.
	fanOutMetrics.reg = reg
	cacheMetrics.reg = reg

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

	// Inbound publisher authentication (spec §Signature verification). nil
	// when the operator opted out via auth.disabled, in which case Middleware
	// is a pass-through.
	inboundAuth, authErr := router.NewInboundAuth(cfg.Auth,
		router.WithAuthMetrics(&authMetricsAdapter{reg: reg}),
		router.WithAuthLogger(slog.Default()),
	)
	if authErr != nil {
		slog.Error("invalid inbound authentication configuration", "error", authErr)
		os.Exit(1)
	}
	if inboundAuth == nil {
		slog.Warn("inbound publisher authentication is disabled — the spec requires the router to authenticate publisher requests before signing and fanning out; enforce it upstream (mesh mTLS, ingress auth) or set TMP_ROUTER_AUTH_API_KEYS")
	}

	mux := http.NewServeMux()
	mux.Handle("POST /tmp/context", inboundAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "context")
		r.HandleContextMatch(w, req)
		elapsed := time.Since(start)
		reg.HistogramObserve("router_request_duration_seconds", elapsed.Seconds(), "context")
		reg.HistogramObserve("tmp_context_match_duration_ms", float64(elapsed.Milliseconds()))
		// Sanitize before logging: the header is caller-supplied, and
		// SafeRequestIDForEcho drops control bytes that would otherwise reach
		// operator logs and anything downstream that re-echoes them.
		//nolint:gosec // G706: value is sanitized by SafeRequestIDForEcho, which gosec's taint analysis cannot see through
		slog.Debug("context match", "request_id", tmproto.SafeRequestIDForEcho(req.Header.Get("X-Request-ID")), "latency_ms", elapsed.Milliseconds())
	})))
	mux.Handle("POST /tmp/identity", inboundAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		reg.CounterInc("router_requests_total", "identity")
		r.HandleIdentityMatch(w, req)
		elapsed := time.Since(start)
		reg.HistogramObserve("router_request_duration_seconds", elapsed.Seconds(), "identity")
		reg.HistogramObserve("tmp_identity_match_duration_ms", float64(elapsed.Milliseconds()))
		//nolint:gosec // G706: value is sanitized by SafeRequestIDForEcho, which gosec's taint analysis cannot see through
		slog.Debug("identity match", "request_id", tmproto.SafeRequestIDForEcho(req.Header.Get("X-Request-ID")), "latency_ms", elapsed.Milliseconds())
	})))
	// Unauthenticated by design: providers fetch the snapshot to resolve the
	// router's signing keys, so requiring a publisher credential here would
	// break signature verification on the fan-out.
	mux.HandleFunc("GET /registry/snapshot", registry.HandleSnapshot)
	// /healthz is the route the spec advertises; /health is kept for back-compat
	// with existing probes that pre-date the spec wording. Both stay on the
	// protocol listener — that is the address load balancers probe.
	healthHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	}
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /health", healthHandler)

	// Operator endpoints. /metrics is spec-named but the spec does not pin it to
	// a listener, and /providers is not in the spec at all — it is an adcp-go
	// extra that returns the full provider registration set (endpoints,
	// audience keys, package allowlists) plus per-provider health. Neither
	// belongs on a publicly reachable address.
	//
	// When admin_addr is set they move to a second listener, mirroring how the
	// identity and context agents split ADMIN_PORT while keeping /health on the
	// protocol listener. When it is unset they stay on the main mux so existing
	// deployments and scrapers keep working; operators must then restrict them
	// at the network layer.
	//
	// Deliberately NOT behind inbound publisher authentication: that credential
	// authorizes a publisher to ask for a match, and using it here would let any
	// authenticated publisher enumerate every other provider's configuration.
	// Operator access is a different trust domain.
	operatorMux := mux
	if cfg.AdminEnabled() {
		operatorMux = http.NewServeMux()
	}
	operatorMux.Handle("GET /metrics", reg.Handler())
	operatorMux.Handle("GET /providers", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	}))

	clientCAs, caErr := cfg.Auth.ClientCAPool()
	if caErr != nil {
		slog.Error("invalid client CA configuration", "error", caErr)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Admin listener, when configured. Always cleartext HTTP and never given the
	// client-CA pool: it is meant to be bound to a private address or a
	// localhost-only sidecar port, not exposed with the publisher-facing TLS
	// material.
	var adminSrv *http.Server
	if cfg.AdminEnabled() {
		adminSrv = &http.Server{
			Addr:         cfg.AdminAddr,
			Handler:      operatorMux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			slog.Info("admin listener starting", "addr", cfg.AdminAddr, "endpoints", "/metrics, /providers")
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("admin listen error", "error", err)
			}
		}()
	} else {
		slog.Warn("/metrics and /providers are on the public listener — set TMP_ROUTER_ADMIN_ADDR to move them to a private one, or restrict them at the network layer; /providers discloses provider endpoints and audience keys")
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
		if bridge != nil {
			bridge.Shutdown()
		}
		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("shutdown error", "error", err)
		}
		// Admin last: keeping /metrics scrapeable while the protocol listener
		// drains means the shutdown itself stays observable.
		if adminSrv != nil {
			if err := adminSrv.Shutdown(ctx); err != nil {
				slog.Error("admin shutdown error", "error", err)
			}
		}
		close(done)
	}()

	slog.Info("TMP Router starting",
		"addr", cfg.Addr,
		"providers", len(cfg.Providers),
		"tls", cfg.TLS.Enabled(),
		"version", version,
	)
	serveErr := listenAndServe(srv, cfg.TLS, clientCAs)
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
//
// When HTTPS is served, TLS 1.2 is pinned as the floor explicitly so future
// Go crypto/tls default changes or GODEBUG overrides cannot silently lower
// it for a binary whose stated purpose is public HTTPS. Leaving TLSNextProto
// nil lets ListenAndServeTLS auto-configure HTTP/2, so the publisher→router
// hop negotiates h2 via ALPN (spec §Transport).
//
// clientCAs, when non-nil, makes a verified client certificate mandatory —
// the mTLS half of publisher→router authentication. ServerConfig.Validate has
// already established that TLS is terminated here whenever clientCAs is set.
func listenAndServe(srv *http.Server, tlsCfg router.TLSConfig, clientCAs *x509.CertPool) error {
	if tlsCfg.Enabled() {
		if srv.TLSConfig == nil {
			srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else if srv.TLSConfig.MinVersion < tls.VersionTLS12 {
			slog.Warn("clamping tls.MinVersion to TLS 1.2 — TLS 1.0/1.1 are not accepted for public HTTPS",
				"previous", srv.TLSConfig.MinVersion,
			)
			srv.TLSConfig.MinVersion = tls.VersionTLS12
		}
		if clientCAs != nil {
			srv.TLSConfig.ClientCAs = clientCAs
			srv.TLSConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
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
			//nolint:gosec // G706: the path comes from the operator's own process environment, not from a request
			slog.Error("failed to load config from TMP_ROUTER_CONFIG", "path", envConfig, "error", err)
			os.Exit(1)
		}
	} else {
		cfg = router.DefaultServerConfig()
	}

	applyEnvOverrides(cfg, addr, os.Getenv)

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid router configuration", "error", err)
		os.Exit(1)
	}

	return cfg
}

// applyEnvOverrides layers env-var and flag overrides onto a base config
// per the flags > env > JSON > defaults rule. Pulled out of loadConfig so
// it can be unit-tested against a synthetic getenv without touching real
// process env or os.Exit. Bad env values fall through to zero-value and
// let downstream validation surface the error (mirrors how signing/TLS
// config paths were already handled inline).
func applyEnvOverrides(cfg *router.ServerConfig, addrFlag string, getenv func(string) string) {
	// Addr — flag beats env beats JSON.
	if addrFlag != "" {
		cfg.Addr = addrFlag
	} else if v := getenv("TMP_ROUTER_ADDR"); v != "" {
		cfg.Addr = v
	}
	// Admin listener for the operator endpoints (/metrics, /providers).
	if v := getenv("TMP_ROUTER_ADMIN_ADDR"); v != "" {
		cfg.AdminAddr = v
	}

	// Inbound authentication — env vars override JSON. Leaving all three
	// unset makes ServerConfig.Validate fail closed; TMP_ROUTER_AUTH_DISABLED
	// is the explicit opt-out for deployments that authenticate upstream.
	if v := getenv("TMP_ROUTER_AUTH_DISABLED"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Auth.Disabled = true
	}
	if v := getenv("TMP_ROUTER_AUTH_API_KEYS"); v != "" {
		cfg.Auth.APIKeys = splitAndTrim(v)
	}
	if v := getenv("TMP_ROUTER_AUTH_KEY_HEADER"); v != "" {
		cfg.Auth.KeyHeader = v
	}
	if v := getenv("TMP_ROUTER_AUTH_CLIENT_CA"); v != "" {
		cfg.Auth.ClientCAPath = v
	}

	// Signing config — env vars override JSON, no flags exposed today.
	if v := getenv("TMP_ROUTER_SIGNING_KID"); v != "" {
		cfg.Signing.KeyID = v
	}
	if v := getenv("TMP_ROUTER_SIGNING_KEY_PATH"); v != "" {
		cfg.Signing.PrivateKeyPath = v
	}
	if v := getenv("TMP_ROUTER_SIGNING_PROPERTY_RIDS"); v != "" {
		cfg.Signing.PropertyRIDs = splitAndTrim(v)
	}
	if v := getenv("TMP_ROUTER_SIGNING_DISABLED"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Signing.Disabled = true
	}

	// TLS config — env vars override JSON; both cert and key must be set to
	// enable HTTPS. Leaving both unset serves cleartext HTTP (typical when
	// TLS is terminated by an upstream ingress).
	if v := getenv("TMP_ROUTER_TLS_CERT"); v != "" {
		cfg.TLS.CertPath = v
	}
	if v := getenv("TMP_ROUTER_TLS_KEY"); v != "" {
		cfg.TLS.KeyPath = v
	}

	// Registry feed — env vars override JSON. Leaving FeedURL empty keeps
	// the router in seed-only mode (dev default).
	if v := getenv("TMP_ROUTER_REGISTRY_FEED_URL"); v != "" {
		cfg.Registry.FeedURL = v
	}
	if v := getenv("TMP_ROUTER_REGISTRY_FEED_TOKEN"); v != "" {
		cfg.Registry.FeedToken = v
	}
	if v := getenv("TMP_ROUTER_REGISTRY_POLL_INTERVAL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Registry.PollIntervalSeconds = n
		}
	}
	if v := getenv("TMP_ROUTER_REGISTRY_BOOTSTRAP_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Registry.BootstrapLimit = n
		}
	}
	if v := getenv("TMP_ROUTER_REGISTRY_FEED_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Registry.FeedLimit = n
		}
	}

	// Cache config — env vars override JSON. TMP_ROUTER_CACHE_DISABLED
	// turns the per-provider Context Match cache off entirely;
	// TMP_ROUTER_CACHE_DEFAULT_TTL_SEC overrides the fallback TTL that
	// applies when a provider response omits cache_ttl.
	if v := getenv("TMP_ROUTER_CACHE_DISABLED"); v == "1" || strings.EqualFold(v, "true") {
		cfg.Cache.Disabled = true
	}
	if v := getenv("TMP_ROUTER_CACHE_DEFAULT_TTL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Cache.DefaultTTLSeconds = n
		}
	}
	if v := getenv("TMP_ROUTER_CACHE_MAX_ENTRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Cache.MaxEntries = n
		}
	}
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

// authMetricsAdapter bridges router.AuthMetrics to prommetrics. The reason
// label is drawn from the router.AuthReject* constants, so cardinality is
// fixed regardless of caller behavior.
type authMetricsAdapter struct {
	reg *prommetrics.Registry
}

func (a *authMetricsAdapter) IncAuthRejected(reason string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_router_auth_rejected_total", reason)
	}
}

// contextCacheMetricsAdapter bridges router.ContextCacheMetrics to
// prommetrics. Provider IDs are stable configured identifiers (spec
// §Provider Registration limits to ^[A-Za-z0-9_]+$, max 64) so the
// label cardinality is bounded by the operator's provider list.
type contextCacheMetricsAdapter struct {
	reg *prommetrics.Registry
}

func (a *contextCacheMetricsAdapter) IncHit(providerID string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_context_cache_hits_total", providerID)
	}
}

func (a *contextCacheMetricsAdapter) IncMiss(providerID string) {
	if a.reg != nil {
		a.reg.CounterInc("tmp_context_cache_misses_total", providerID)
	}
}
