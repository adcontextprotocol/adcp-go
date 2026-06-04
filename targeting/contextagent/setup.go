package contextagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/netutil"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// SuppressionStaleThreshold is the consecutive-failure count at which
// /live flips to 503 for the suppression-snapshot liveness check. With
// the default 5-minute refresh interval, 6 consecutive failures means
// ~30 minutes of running on the snapshot the agent had at the last
// success — long enough that an operator wants the pod recycled rather
// than continuing to serve traffic with a kill-switch list that may
// no longer be authoritative.
const SuppressionStaleThreshold = 6

// SuppressionStaleMaxAge is the upper bound on how old the in-memory
// suppression snapshot can be before /live flips to 503, independent
// of the consecutive-failure counter. Covers the failure mode where
// the refresh-loop goroutine exits cleanly (e.g. its context is
// cancelled by a future plumbing change) without ever incrementing
// ConsecutiveFailures — in that scenario the snapshot freezes
// forever and the counter-only check reports healthy. 30 minutes
// matches the failure-count threshold's effective coverage at the
// default 5-minute refresh interval; an operator running with a
// longer SUPPRESSION_REFRESH_INTERVAL should keep this longer than
// 2× their interval.
const SuppressionStaleMaxAge = 30 * time.Minute

// Run executes the agent lifecycle: build dependencies, start the
// HTTP server, then block until SIGINT/SIGTERM and run an orderly
// shutdown. Returns non-nil only when startup fails or shutdown
// surfaces errors.
func Run(ctx context.Context, cfg Config, logger *slog.Logger, version string) (retErr error) {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	metricsProvider, err := BuildMetrics(cfg.Metrics)
	if err != nil {
		return fmt.Errorf("build metrics provider: %w", err)
	}
	// Tear the metrics provider down on any startup-path return.
	// Without this, a buildBundle failure or any later registration
	// error leaks the OTEL meter provider's background flush
	// goroutine and the Prometheus registry. The success path
	// neutralizes the defer by setting metricsCleanupRan; the regular
	// shutdown sequence below then calls Shutdown explicitly.
	metricsCleanupRan := false
	defer func() {
		if retErr == nil || metricsCleanupRan {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = metricsProvider.Shutdown(shutdownCtx)
	}()
	recorder := metricsProvider.Recorder

	bundle, err := buildBundle(ctx, cfg, recorder, logger)
	if err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	contextHandler := NewHandler(HandlerConfig{
		Engine:                     bundle.engine,
		RequestTimeout:             cfg.RequestTimeout,
		RequestBodyLimit:           int64(cfg.RequestBodyLimitBytes),
		ResponseTTL:                cfg.ResponseTTL,
		SupportedADCPMajorVersions: cfg.SupportedADCPMajorVersions,
		Recorder:                   recorder,
		Logger:                     logger,
	})

	requireSig := !cfg.TMP.AllowUnsigned
	if !requireSig {
		logger.Warn("/context accepts unsigned requests — TMP signing should be required in production")
	}
	if requireSig && cfg.TMP.OwnEndpointURL == "" {
		return errors.New("TMP_OWN_ENDPOINT_URL is required when signature verification is enabled")
	}

	liveness := []LivenessCheck{
		{
			Name: "suppression_snapshot",
			Fn: func() error {
				if f := bundle.suppressionSnap.ConsecutiveFailures(); f >= SuppressionStaleThreshold {
					return fmt.Errorf("suppression snapshot has not refreshed in %d consecutive attempts", f)
				}
				// Belt-and-braces age check: catches the case where
				// the refresh-loop goroutine exits without ever
				// touching the failure counter (e.g. a future plumbing
				// change cancels its ctx). LastSuccessfulRefresh is
				// the zero time before the first Load, which Start
				// already guarantees has completed before reaching
				// here.
				last := bundle.suppressionSnap.LastSuccessfulRefresh()
				if !last.IsZero() && time.Since(last) > SuppressionStaleMaxAge {
					return fmt.Errorf("suppression snapshot last refreshed %s ago (>%s)", time.Since(last).Round(time.Second), SuppressionStaleMaxAge)
				}
				return nil
			},
		},
	}

	var running atomic.Bool
	srv := NewServer(ServerConfig{
		Port:              cfg.HTTPPort,
		ContextHandler:    contextHandler,
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
		RequestBodyLimit:  int64(cfg.RequestBodyLimitBytes),
		AdminPort:         cfg.AdminPort,
		StrictContentType: cfg.StrictContentType,
		LivenessChecks:    liveness,
		Recorder:          recorder,
		Logger:            logger,
	})

	var adminSrv *http.Server
	if cfg.AdminPort > 0 {
		adminSrv = NewAdminServer(AdminServerConfig{
			Port:              cfg.AdminPort,
			Registry:          metricsProvider.Registry,
			Version:           version,
			IsRunning:         running.Load,
			PprofEnabled:      cfg.Pprof.Enabled,
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			LivenessChecks:    liveness,
			Recorder:          recorder,
			Logger:            logger,
		})
	}

	tracker := &connTracker{}
	if err := metricsProvider.RegisterOpenConnectionsObserver(tracker.Open); err != nil {
		teardownBundle(bundle)
		return fmt.Errorf("register open-connections observer: %w", err)
	}
	if err := metricsProvider.RegisterSuppressionSnapshotObservers(
		func() int64 { return int64(bundle.suppressionSnap.ConsecutiveFailures()) },
		func() int64 { return bundle.suppressionSnap.LastSuccessfulRefresh().Unix() },
		func() int64 { p, _ := bundle.suppressionSnap.Sizes(); return int64(p) },
		func() int64 { _, g := bundle.suppressionSnap.Sizes(); return int64(g) },
	); err != nil {
		teardownBundle(bundle)
		return fmt.Errorf("register suppression observers: %w", err)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	baseLn, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		teardownBundle(bundle)
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	// Compose: raw listener → LimitListener (caps concurrent accepts)
	// → trackingListener (counts opens for the open_connections gauge).
	limitedLn := netutil.LimitListener(baseLn, cfg.MaxOpenConnections)
	ln := &trackingListener{Listener: limitedLn, tracker: tracker}

	serverErr := make(chan error, 2)
	// Buffered non-blocking send so a panic in Serve cannot deadlock
	// the recover defer if the main loop has already consumed an
	// error from the same channel.
	pushServerErr := func(err error) {
		select {
		case serverErr <- err:
		default:
		}
	}
	safeGoWithPanicSink(logger, recorder, "http-server", pushServerErr, func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			pushServerErr(err)
		}
	})
	if adminSrv != nil {
		// Admin listener gets its own bounded concurrency so a
		// misbehaving Prometheus scraper / pprof poller can't
		// exhaust the FD budget independently of the main listener.
		// Reuse MaxOpenConnections — operators tune the main agent's
		// budget, and the admin surface is much lower-volume so the
		// same ceiling is conservative.
		adminLn, err := net.Listen("tcp", adminSrv.Addr)
		if err != nil {
			// The main server is already serving on baseLn — drain
			// it via Shutdown so in-flight requests don't see a
			// torn-down keystore / Valkey pool. teardownBundle then
			// closes the bundle's background goroutines and Valkey
			// pool so this return doesn't leak them.
			_ = srv.Shutdown(context.Background())
			teardownBundle(bundle)
			return fmt.Errorf("listen admin %s: %w", adminSrv.Addr, err)
		}
		boundedAdmin := netutil.LimitListener(adminLn, cfg.MaxOpenConnections)
		safeGoWithPanicSink(logger, recorder, "admin-http-server", pushServerErr, func() {
			if err := adminSrv.Serve(boundedAdmin); err != nil && !errors.Is(err, http.ErrServerClosed) {
				pushServerErr(err)
			}
		})
	}

	running.Store(true)
	logger.Info("context agent started",
		"addr", srv.Addr,
		"version", version,
		"seller_agent_url", cfg.SellerAgentURL,
		"provider_id", cfg.ProviderID,
	)

	var startupErr error
	select {
	case sig := <-quit:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			logger.Error("server returned error", "error", err)
			startupErr = err
		}
	case <-ctx.Done():
		logger.Info("shutting down", "reason", ctx.Err().Error())
	}

	running.Store(false)
	if cfg.ShutdownGrace > 0 {
		select {
		case <-time.After(cfg.ShutdownGrace):
		case <-quit:
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	var shutdownErrs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("server: %w", err))
	}
	if adminSrv != nil {
		if err := adminSrv.Shutdown(shutdownCtx); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("admin server: %w", err))
		}
	}
	bundle.cancelBackground()
	if bundle.valkeyCloser != nil {
		if err := bundle.valkeyCloser.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("valkey: %w", err))
		}
	}
	if err := metricsProvider.Shutdown(shutdownCtx); err != nil {
		shutdownErrs = append(shutdownErrs, fmt.Errorf("metrics: %w", err))
	}
	metricsCleanupRan = true

	return errors.Join(startupErr, errors.Join(shutdownErrs...))
}

// bundle holds every dependency built by buildBundle so Run can manage
// their lifetimes.
type bundle struct {
	engine           *targeting.ContextEngine
	keystore         tmproto.KeyStore
	suppressionSnap  *suppressionstore.Snapshot
	valkeyCloser     interface{ Close() error }
	cancelBackground context.CancelFunc
}

// teardownBundle releases the background goroutines and the Valkey
// pool a successful buildBundle constructs. Used on every Run
// early-return path between buildBundle success and the steady-state
// shutdown sequence so a startup-time failure (observer registration,
// listener bind) does not leak the keystore-refresh goroutine, the
// suppression-refresh goroutine, or the Valkey connection pool.
// Idempotent: safe to call once per Run invocation; the steady-state
// shutdown path inlines the same steps and does not call this.
func teardownBundle(b *bundle) {
	if b == nil {
		return
	}
	if b.cancelBackground != nil {
		b.cancelBackground()
	}
	if b.valkeyCloser != nil {
		_ = b.valkeyCloser.Close()
	}
}

func buildBundle(ctx context.Context, cfg Config, recorder Recorder, logger *slog.Logger) (*bundle, error) {
	bgCtx, cancelBg := context.WithCancel(context.Background())

	// Valkey backend. *redisstore.Store satisfies every per-domain
	// Store interface (mediabuystore.Store, pkgconfigstore.Store,
	// urlliststore.Store, suppressionstore.Store, topicstore.Store)
	// via duck typing — every required method is implemented on the
	// same concrete struct.
	rawStore, valkeyCloser, err := redisstore.Build(ctx, cfg.Valkey.ToRedisStoreConfig())
	if err != nil {
		cancelBg()
		return nil, fmt.Errorf("valkey: %w", err)
	}

	suppressSnap, err := suppressionstore.NewSnapshot(suppressionstore.SnapshotConfig{
		Store:      rawStore,
		ProviderID: cfg.ProviderID,
		Logger:     logger,
	})
	if err != nil {
		_ = valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("suppression snapshot: %w", err)
	}
	if err := suppressSnap.Start(bgCtx, cfg.SuppressionRefreshInterval); err != nil {
		// Fail-closed: suppressions are kill switches. Starting with
		// an empty snapshot means serving traffic on every property /
		// geo until the next refresh — for a Valkey that's actually
		// down at boot, that could be the entire pod lifetime. Refuse
		// to start so the orchestrator restarts the pod and tries
		// again, rather than entering a silently-degraded steady
		// state.
		_ = valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("suppression initial load: %w", err)
	}

	storage, err := buildStorage(
		rawStore,
		rawStore,
		rawStore,
		rawStore,
		suppressSnap,
		cfg.Cache,
		logger,
	)
	if err != nil {
		_ = valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("storage: %w", err)
	}

	if len(cfg.PropertyRIDs) == 0 {
		logger.Warn("PROPERTY_RIDS is empty; every inbound request will short-circuit on the global property bitmap. Wire up the registry feed before serving traffic.")
	}
	engine := targeting.NewContextEngine(targeting.ContextEngineConfig{
		ProviderID:         cfg.ProviderID,
		SellerAgentURL:     cfg.SellerAgentURL,
		Properties:         targeting.PropertyList{Global: targeting.NewMapBitmap(cfg.PropertyRIDs...)},
		Storage:            storage,
		AcceptedTaxonomies: cfg.AcceptedTaxonomies,
		Metrics:            newTargetingMetricsAdapter(recorder),
	})

	keystore, err := buildKeyStore(bgCtx, cfg.TMP, recorder, logger)
	if err != nil {
		_ = valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("keystore: %w", err)
	}

	return &bundle{
		engine:           engine,
		keystore:         keystore,
		suppressionSnap:  suppressSnap,
		valkeyCloser:     valkeyCloser,
		cancelBackground: cancelBg,
	}, nil
}

// buildKeyStore wires up the TMP signature key store from
// TMPConfig.RegistryURL. The background refresh runs under safeGo so a
// panic in the upstream library is captured and reported instead of
// taking the process down.
func buildKeyStore(ctx context.Context, cfg TMPConfig, recorder Recorder, logger *slog.Logger) (tmproto.KeyStore, error) {
	if cfg.RegistryURL == "" {
		if !cfg.AllowUnsigned {
			return nil, errors.New("TMP_REGISTRY_URL is required when signature verification is enabled")
		}
		return nil, nil
	}
	ks, err := tmproto.NewRemoteKeyStore(tmproto.RemoteKeyStoreOptions{URL: cfg.RegistryURL})
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := ks.Refresh(fetchCtx); err != nil {
		if recorder != nil {
			recorder.KeystoreRefresh(ctx, OutcomeError)
		}
		return nil, fmt.Errorf("initial registry fetch: %w", err)
	}
	if recorder != nil {
		recorder.KeystoreRefresh(ctx, OutcomePass)
	}
	safeGo(logger, recorder, "keystore-refresh", func() {
		if err := ks.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("registry keystore Run terminated", "error", err)
		}
	})
	return ks, nil
}
