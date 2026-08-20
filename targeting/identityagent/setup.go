package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/netutil"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig/scope3"
	"github.com/adcontextprotocol/adcp-go/targeting/internal/liveramp"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/adcontextprotocol/adcp-go/uid2client"
)

// identity-config retry curve. The deadline and mode are env-configurable
// (see IdentityConfigSourceConfig); these three control the curve shape
// between retries and are stable across deployments. Tune in code if a
// future incident shows the curve itself is wrong.
const (
	configRetryInitial = 1 * time.Second
	configRetryMax     = 30 * time.Second
	configRetryBackoff = identityconfig.BackoffExponential
)

// Run executes the agent lifecycle: build dependencies, start the HTTP
// server, then block until SIGINT/SIGTERM and run an orderly shutdown.
// Returns a non-nil error only when startup fails; once the server is
// running, errors during shutdown are logged and joined into the return
// value but the function still returns.
//
// The supplied logger is used for structured event logs. version is stamped
// into /live responses; /health intentionally omits it per the TMP spec.
func Run(ctx context.Context, cfg Config, logger *slog.Logger, version string, opts ...RunOption) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	startTime := time.Now()

	var ro runOptions
	for _, opt := range opts {
		opt(&ro)
	}

	metricsProvider, err := Build(cfg.Metrics)
	if err != nil {
		return fmt.Errorf("build metrics provider: %w", err)
	}

	bundle, err := buildBundle(ctx, cfg, metricsProvider.Recorder, logger, ro)
	if err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	identityHandler := NewIdentityHandler(IdentityHandlerConfig{
		Service:                    bundle.service,
		TMPXSealer:                 bundle.tmpx,
		Canonicalizer:              bundle.canonicalizer,
		RequestTimeout:             cfg.RequestTimeout,
		RequestBodyLimit:           int64(cfg.RequestBodyLimitBytes),
		ResponseTTL:                cfg.ResponseTTL,
		SupportedADCPMajorVersions: cfg.SupportedADCPMajorVersions,
		Recorder:                   metricsProvider.Recorder,
		Logger:                     logger,
	})

	requireSig := !cfg.TMP.AllowUnsigned
	if !requireSig {
		logger.Warn("/identity accepts unsigned requests — TMP signing should be required in production")
	}
	if requireSig && cfg.TMP.OwnEndpointURL == "" {
		return errors.New("TMP_OWN_ENDPOINT_URL is required when signature verification is enabled")
	}

	running := &runningFlag{}
	srv := NewServer(ServerConfig{
		Port:              cfg.HTTPPort,
		IdentityHandler:   identityHandler,
		KeyStore:          bundle.keystore,
		OwnEndpointURL:    cfg.TMP.OwnEndpointURL,
		RequireSig:        requireSig,
		Registry:          metricsProvider.Registry,
		IsRunning:         running.Load,
		Version:           version,
		PprofEnabled:      cfg.Pprof.Enabled,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		AdminPort:         cfg.AdminPort,
		StrictContentType: cfg.StrictContentType,
		AccessLogEnabled:  cfg.AccessLogEnabled,
		Recorder:          metricsProvider.Recorder,
		Logger:            logger,
	})

	var adminSrv *http.Server
	if cfg.AdminPort > 0 {
		adminSrv = NewAdminServer(AdminServerConfig{
			Port:              cfg.AdminPort,
			Registry:          metricsProvider.Registry,
			Version:           version,
			PprofEnabled:      cfg.Pprof.Enabled,
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			Recorder:          metricsProvider.Recorder,
			Logger:            logger,
		})
	}

	tracker := &connTracker{}
	if err := metricsProvider.RegisterOpenConnectionsObserver(tracker.Open); err != nil {
		return fmt.Errorf("register open-connections observer: %w", err)
	}
	if err := metricsProvider.RegisterConfigEntriesObserver(func() int64 {
		return int64(bundle.configSvc.Len())
	}); err != nil {
		return fmt.Errorf("register config-entries observer: %w", err)
	}

	registry := newShutdownRegistry(logger, metricsProvider.Recorder)
	registry.add("identity-config", func(_ context.Context) error {
		bundle.configSvc.Stop()
		return nil
	})
	registry.add("server", func(c context.Context) error {
		return srv.Shutdown(c)
	})
	if adminSrv != nil {
		registry.add("admin-server", func(c context.Context) error {
			return adminSrv.Shutdown(c)
		})
	}
	if bundle.audienceCloser != nil {
		registry.add("audience-valkey", func(_ context.Context) error {
			return bundle.audienceCloser.Close()
		})
	}
	if bundle.audienceFallbackCloser != nil {
		registry.add("audience-valkey-fallback", func(_ context.Context) error {
			return bundle.audienceFallbackCloser.Close()
		})
	}
	registry.add("fcap-valkey", func(_ context.Context) error {
		return bundle.fcapCloser.Close()
	})
	if bundle.fcapFallbackCloser != nil {
		registry.add("fcap-valkey-fallback", func(_ context.Context) error {
			return bundle.fcapFallbackCloser.Close()
		})
	}
	registry.add("background-refresh", func(_ context.Context) error {
		// Cancels both the TMP keystore and TMPX JWKS refresh goroutines.
		bundle.cancelBackground()
		return nil
	})
	registry.add("metrics", func(c context.Context) error {
		return metricsProvider.Shutdown(c)
	})

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	baseLn, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	// Compose: raw listener → LimitListener (caps concurrent accepts) →
	// trackingListener (counts open conns for the open_connections gauge).
	// LimitListener's Accept blocks when the cap is reached; new SYNs queue
	// in the kernel backlog and are eventually dropped, which is the
	// intended fail mode.
	limitedLn := netutil.LimitListener(baseLn, cfg.MaxOpenConnections)
	ln := &trackingListener{Listener: limitedLn, tracker: tracker}

	// Channel buffered for both servers so neither goroutine blocks when
	// the other has already pushed a fatal error.
	serverErr := make(chan error, 2)
	safeGo(logger, metricsProvider.Recorder, "http-server", func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	})
	if adminSrv != nil {
		// The admin listener is not connection-capped: it serves
		// observability traffic from a small set of known peers
		// (kubelet, Prometheus scraper) and shares its timeouts with
		// the main server. A noisy scrape pattern shouldn't be able
		// to starve identity traffic of FDs.
		safeGo(logger, metricsProvider.Recorder, "admin-http-server", func() {
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		})
	}

	running.Store(true)
	logger.Info("identity agent started",
		"addr", srv.Addr,
		"startup_duration", time.Since(startTime).String(),
		"version", version,
	)

	var startupErr error
	select {
	case sig := <-quit:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			logger.Error("server returned with error", "error", err)
			startupErr = err
		}
	case <-ctx.Done():
		logger.Info("shutting down", "reason", ctx.Err().Error())
	}

	running.Store(false)
	// Give load balancers time to observe the readiness flip and drain
	// existing connections. A second SIGINT/SIGTERM short-circuits the
	// wait so an operator can force a faster shutdown.
	if cfg.ShutdownGrace > 0 {
		select {
		case <-time.After(cfg.ShutdownGrace):
		case <-quit:
			logger.Info("second signal received, skipping shutdown grace")
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	shutdownErr := registry.cancel(shutdownCtx)
	if shutdownErr != nil {
		logger.Error("shutdown tasks finished with errors", "err", shutdownErr)
	}

	// Drain any late errors from the server goroutines. A non-ErrServerClosed
	// return from Serve/ListenAndServe after shutdown is a diagnostic we'd
	// otherwise lose to a dead channel.
	for {
		select {
		case err, ok := <-serverErr:
			if ok && err != nil {
				logger.Error("server goroutine returned with error during shutdown", "error", err)
			}
			if !ok {
				return errors.Join(startupErr, shutdownErr)
			}
		default:
			if startupErr == nil && shutdownErr == nil {
				logger.Info("shutdown complete")
			}
			return errors.Join(startupErr, shutdownErr)
		}
	}
}

// bundle holds the constructed dependencies passed between Run and the
// shutdown registry. cancelBackground stops the keystore and TMPX JWKS
// refresh goroutines together.
type bundle struct {
	service                *Service
	configSvc              *identityconfig.Service
	audienceSvc            *audience.Service
	audienceCloser         interface{ Close() error }
	fcapCloser             interface{ Close() error }
	audienceFallbackCloser interface{ Close() error }
	fcapFallbackCloser     interface{ Close() error }
	keystore               tmproto.KeyStore
	tmpx                   *TMPXSealer
	canonicalizer          *IdentityCanonicalizer
	cancelBackground       context.CancelFunc
}

// buildBundle wires up every dependency the Service needs. Constructed
// resources push their teardown function onto rollback; on a build
// failure rollback runs them in reverse order so partial state is
// released. On success the deferred rollback observes retErr == nil and
// does nothing — the accumulated list is dropped when the function
// returns and never touched. The registered closers are then owned by
// the caller via the shutdown registry (see Run in server.go).
func buildBundle(ctx context.Context, cfg Config, recorder Recorder, logger *slog.Logger, opts runOptions) (b *bundle, retErr error) {
	bgCtx, cancelBg := context.WithCancel(context.Background())

	type rollbackStep struct {
		name string
		fn   func() error
	}
	var rollback []rollbackStep
	addRollback := func(name string, fn func() error) {
		rollback = append(rollback, rollbackStep{name: name, fn: fn})
	}
	defer func() {
		if retErr == nil {
			return
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			step := rollback[i]
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						logger.Error("rollback step panicked",
							"step", step.name, "panic", fmt.Sprintf("%v", rec))
					}
				}()
				if err := step.fn(); err != nil {
					logger.Warn("rollback step error", "step", step.name, "error", err)
				}
			}()
		}
		cancelBg()
	}()

	// Identity-config service (refreshed periodically).
	source, err := scope3.New(
		cfg.IdentityConfig.URL,
		cfg.IdentityConfig.Token,
		scope3.WithHTTPTimeout(cfg.IdentityConfig.Timeout),
		scope3.WithExtraHeaders(cfg.IdentityConfig.ExtraHeaders),
	)
	if err != nil {
		return nil, fmt.Errorf("init scope3 source: %w", err)
	}
	configSvc, err := identityconfig.New(source, cfg.IdentityConfig.RefreshInterval,
		identityconfig.WithStartConfig(startConfigFor(cfg.IdentityConfig)),
		identityconfig.WithLogger(logger),
		identityconfig.WithRefreshObserver(func(outcome string) {
			recorder.ConfigRefresh(context.Background(), outcome)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("init identityconfig service: %w", err)
	}
	if err := configSvc.Start(ctx); err != nil {
		return nil, fmt.Errorf("identityconfig initial load: %w", err)
	}
	addRollback("identity-config", func() error { configSvc.Stop(); return nil })

	// Fcap valkey (required). If a fallback block is configured, wrap the
	// primary store in a union-read adapter — reads OR the two stores so
	// answers stay correct across a shadow-mode resharding.
	fcapPrimaryStore, fcapCloser, err := redisstore.Build(ctx, cfg.FCapValkey.ToRedisStoreConfig())
	if err != nil {
		return nil, fmt.Errorf("fcap valkey: %w", err)
	}
	addRollback("fcap-valkey", fcapCloser.Close)
	var (
		fcapStore         fcap.Store = fcapPrimaryStore
		fcapFallbackClose interface{ Close() error }
	)
	if cfg.FallbackFCapValkey.Enabled {
		fallbackStore, fallbackCloser, err := redisstore.Build(ctx, cfg.FallbackFCapValkey.ToRedisStoreConfig())
		if err != nil {
			return nil, fmt.Errorf("fcap valkey (fallback): %w", err)
		}
		addRollback("fcap-valkey-fallback", fallbackCloser.Close)
		fcapFallbackClose = fallbackCloser
		fcapStore = &unionFcapStore{primary: fcapPrimaryStore, fallback: fallbackStore}
		logger.Info("fcap valkey fallback enabled — reads will OR across two topologies until FCAP_FALLBACK_VALKEY_* is removed")
	}
	fcapSvc := fcap.New(fcapStore)

	// Audience valkey (optional). Same fallback wrapping as fcap.
	var (
		audienceSvc            *audience.Service
		audienceCloser         interface{ Close() error }
		audienceFallbackCloser interface{ Close() error }
	)
	if cfg.AudienceValkey.Enabled {
		primaryStore, closer, err := redisstore.Build(ctx, cfg.AudienceValkey.ToRedisStoreConfig())
		if err != nil {
			return nil, fmt.Errorf("audience valkey: %w", err)
		}
		addRollback("audience-valkey", closer.Close)
		var audienceStore audience.Store = primaryStore
		if cfg.FallbackAudienceValkey.Enabled {
			fallbackStore, fallbackCloser, err := redisstore.Build(ctx, cfg.FallbackAudienceValkey.ToRedisStoreConfig())
			if err != nil {
				return nil, fmt.Errorf("audience valkey (fallback): %w", err)
			}
			addRollback("audience-valkey-fallback", fallbackCloser.Close)
			audienceFallbackCloser = fallbackCloser
			audienceStore = &unionAudienceStore{primary: primaryStore, fallback: fallbackStore}
			logger.Info("audience valkey fallback enabled — reads will OR across two topologies until AUDIENCE_FALLBACK_VALKEY_* is removed")
		}
		audienceSvc = audience.New(audienceStore)
		audienceCloser = closer
	}

	engine := targeting.NewIdentityEngine(targeting.IdentityEngineConfig{
		Audience: audienceSvc,
		Metrics:  newTargetingMetricsAdapter(recorder),
	})

	svc, err := NewService(ServiceConfig{
		Engine:          engine,
		FCap:            fcapSvc,
		Audience:        audienceSvc,
		ConfigService:   configSvc,
		FCapTimeout:     cfg.FCapTimeout,
		AudienceTimeout: cfg.AudienceTimeout,
		Recorder:        recorder,
		Verifier:        opts.verifier,
		RecipientKeys:   opts.recipientKeys,
		AgeResolver:     opts.ageResolver,
		RelyingPartyID:  opts.relyingPartyID,
	})
	if err != nil {
		return nil, fmt.Errorf("build service: %w", err)
	}

	keystore, err := BuildKeyStore(bgCtx, cfg.TMP, logger, recorder)
	if err != nil {
		return nil, fmt.Errorf("keystore: %w", err)
	}

	// lrSidecar stays nil-typed as the interface so NewTMPXSealer's nil
	// check works — assigning a typed-nil *liveramp.Client to an interface
	// variable would make the interface compare != nil.
	var lrSidecar LiveRampSidecar
	if cfg.LiveRamp.Enabled() {
		c, lrErr := liveramp.NewClient(liveramp.Config{
			URL:         cfg.LiveRamp.URL,
			Timeout:     cfg.LiveRamp.Timeout,
			DialTimeout: cfg.LiveRamp.DialTimeout,
		})
		if lrErr != nil {
			return nil, fmt.Errorf("liveramp client: %w", lrErr)
		}
		lrSidecar = c
		logger.Info("LiveRamp sidecar enabled", "url", cfg.LiveRamp.URL)
	} else {
		logger.Info("LiveRamp sidecar disabled — RampID and RampID-derived identities will be ignored in TMPX tokens")
	}

	uid2Client, err := buildUID2Client(bgCtx, cfg.UID2, uid2client.ScopeUID2, "UID2", logger)
	if err != nil {
		return nil, fmt.Errorf("uid2 client: %w", err)
	}
	euidClient, err := buildUID2Client(bgCtx, cfg.EUID, uid2client.ScopeEUID, "EUID", logger)
	if err != nil {
		return nil, fmt.Errorf("euid client: %w", err)
	}

	tmpx, err := NewTMPXSealerWithOptions(bgCtx, cfg.TMPX, logger, recorder,
		WithLiveRampSidecar(lrSidecar),
		WithUID2Operator(uid2Client),
		WithEUIDOperator(euidClient),
	)
	if err != nil {
		return nil, fmt.Errorf("tmpx: %w", err)
	}

	// The canonicalizer is constructed independently of TMPX configuration:
	// even deployments that never emit a TMPX token need audience/fcap
	// lookups keyed on the canonical decoded form so they see the same
	// shape ExposureLog.user_token publishes downstream. The same
	// LiveRamp sidecar (when configured) backs RampID decoding here as in
	// the sealer.
	canonicalizer := NewIdentityCanonicalizerWithOptions(logger, recorder,
		WithLiveRampSidecar(lrSidecar),
		WithUID2Operator(uid2Client),
		WithEUIDOperator(euidClient),
	)

	return &bundle{
		service:                svc,
		configSvc:              configSvc,
		audienceSvc:            audienceSvc,
		audienceCloser:         audienceCloser,
		fcapCloser:             fcapCloser,
		audienceFallbackCloser: audienceFallbackCloser,
		fcapFallbackCloser:     fcapFallbackClose,
		keystore:               keystore,
		tmpx:                   tmpx,
		canonicalizer:          canonicalizer,
		cancelBackground:       cancelBg,
	}, nil
}

// buildUID2Client constructs a uid2client.Client for the supplied
// operator config, or returns nil when the config is disabled. logger
// prefix identifies the scope for startup logs. A non-nil error means
// the config was populated but the initial refresh failed — startup
// must not proceed with a half-configured UID2/EUID surface.
//
// The returned value is a UID2Operator interface (not a concrete
// *uid2client.Client) so callers pass it directly to NewTMPXSealer /
// NewIdentityCanonicalizer without triggering the typed-nil trap: when
// the config is disabled we return an untyped nil.
func buildUID2Client(lifetimeCtx context.Context, cfg UID2OperatorConfig, scope uid2client.IdentityScope, label string, logger *slog.Logger) (UID2Operator, error) {
	if !cfg.Enabled() {
		logger.Info(label+" operator client disabled — tokens of this scope will be ignored",
			"reason", "credentials not configured")
		return nil, nil
	}
	client, err := uid2client.New(lifetimeCtx, uid2client.Config{
		OperatorURL:        cfg.OperatorURL,
		APIKey:             cfg.APIKey,
		ClientSecret:       cfg.ClientSecret,
		IdentityScope:      scope,
		HTTPTimeout:        cfg.HTTPTimeout,
		KeyRefreshInterval: cfg.KeyRefreshInterval,
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	logger.Info(label+" operator client enabled",
		"operator_url", cfg.OperatorURL,
		"key_refresh_interval", cfg.KeyRefreshInterval.String())
	return client, nil
}

// startConfigFor maps the env-configurable IdentityConfigSourceConfig onto
// the identityconfig.StartConfig the Service.Start expects. Unknown
// StartMode values fall through to retry — Config.Validate rejects them at
// startup, so this branch is unreachable in normal operation.
func startConfigFor(cfg IdentityConfigSourceConfig) identityconfig.StartConfig {
	switch cfg.StartMode {
	case StartModeFailFast:
		return identityconfig.StartConfig{Mode: identityconfig.StartModeFailFast}
	case StartModeBestEffort:
		return identityconfig.StartConfig{Mode: identityconfig.StartModeBestEffort}
	}
	return identityconfig.StartConfig{
		Mode: identityconfig.StartModeRetry,
		Retry: identityconfig.RetryConfig{
			Initial:  configRetryInitial,
			Max:      configRetryMax,
			Backoff:  configRetryBackoff,
			Deadline: cfg.StartRetryDeadline,
		},
	}
}
