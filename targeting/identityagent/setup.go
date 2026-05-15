package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig"
	"github.com/adcontextprotocol/adcp-go/targeting/identityconfig/scope3"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// MaxShutdownDuration is the hard upper bound on graceful shutdown. After
// this elapses the process exits regardless of pending shutdown tasks.
const MaxShutdownDuration = 10 * time.Second

// Run executes the agent lifecycle: build dependencies, start the HTTP
// server, then block until SIGINT/SIGTERM and run an orderly shutdown.
// Returns a non-nil error only when startup fails; once the server is
// running, errors during shutdown are logged and joined into the return
// value but the function still returns.
//
// The supplied logger is used for structured event logs. version is stamped
// into /live and /health responses.
func Run(ctx context.Context, cfg Config, logger *slog.Logger, version string) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	startTime := time.Now()

	metricsProvider, err := Build(cfg.Metrics)
	if err != nil {
		return fmt.Errorf("build metrics provider: %w", err)
	}

	bundle, err := buildBundle(ctx, cfg, metricsProvider.Recorder, logger)
	if err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	identityHandler := NewIdentityHandler(IdentityHandlerConfig{
		Service:        bundle.service,
		TMPXConfig:     bundle.tmpx,
		RequestTimeout: cfg.RequestTimeout,
		Recorder:       metricsProvider.Recorder,
		Logger:         logger,
	})

	requireSig := !cfg.TMP.AllowUnsigned
	if !requireSig {
		logger.Warn("/tmp/identity accepts unsigned requests — TMP signing should be required in production")
	}
	if requireSig && cfg.TMP.OwnEndpointURL == "" {
		return errors.New("TMP_OWN_ENDPOINT_URL is required when signature verification is enabled")
	}

	running := &runningFlag{}
	srv := NewServer(ServerConfig{
		Port:            cfg.HTTPPort,
		IdentityHandler: identityHandler,
		KeyStore:        bundle.keystore,
		OwnEndpointURL:  cfg.TMP.OwnEndpointURL,
		RequireSig:      requireSig,
		Registry:        metricsProvider.Registry,
		IsRunning:       running.Load,
		Version:         version,
		PprofEnabled:    cfg.Pprof.Enabled,
	})

	if cfg.AudienceValkey.Enabled || bundle.audienceSvc == nil {
		if !cfg.AudienceValkey.Enabled && bundle.hasSegmentRules {
			logger.Warn("AUDIENCE_VALKEY is unconfigured but identity-config contains packages with segment rules; those packages will be ineligible at request time")
		}
	}

	registry := newShutdownRegistry(logger)
	registry.add("identity-config", func(_ context.Context) error {
		bundle.configSvc.Stop()
		return nil
	})
	registry.add("server", func(c context.Context) error {
		return srv.Shutdown(c)
	})
	if bundle.audienceCloser != nil {
		registry.add("audience-valkey", func(_ context.Context) error {
			return bundle.audienceCloser.Close()
		})
	}
	registry.add("fcap-valkey", func(_ context.Context) error {
		return bundle.fcapCloser.Close()
	})
	registry.add("keystore", func(_ context.Context) error {
		bundle.cancelBackground()
		return nil
	})
	registry.add("metrics", func(c context.Context) error {
		return metricsProvider.Shutdown(c)
	})

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	running.Store(true)
	logger.Info("identity agent started",
		"addr", srv.Addr,
		"startup_duration", time.Since(startTime).String(),
		"version", version,
	)

	select {
	case sig := <-quit:
		logger.Info("shutting down", "signal", sig.String())
	case err := <-serverErr:
		if err != nil {
			logger.Error("server failed to start", "error", err)
			bundle.cancelBackground()
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down", "reason", ctx.Err().Error())
	}

	running.Store(false)
	time.Sleep(cfg.ShutdownGrace) // let readiness flip propagate to LBs

	shutdownCtx, cancel := context.WithTimeout(context.Background(), MaxShutdownDuration)
	defer cancel()
	if err := registry.cancel(shutdownCtx); err != nil {
		logger.Error("shutdown tasks finished with errors", "err", err)
		return err
	}
	logger.Info("shutdown complete")
	return nil
}

// bundle holds the constructed dependencies passed between Run and the
// shutdown registry. cancelBackground stops the keystore and TMPX JWKS
// refresh goroutines together.
type bundle struct {
	service          *Service
	configSvc        *identityconfig.Service
	audienceSvc      *audience.Service
	audienceCloser   interface{ Close() error }
	fcapCloser       interface{ Close() error }
	keystore         tmproto.KeyStore
	tmpx             *tmpxConfig
	cancelBackground context.CancelFunc
	hasSegmentRules  bool
}

func buildBundle(ctx context.Context, cfg Config, recorder Recorder, logger *slog.Logger) (*bundle, error) {
	bgCtx, cancel := context.WithCancel(context.Background())
	cleanup := func(err error) error {
		cancel()
		return err
	}

	// Identity-config service (refreshed periodically).
	source, err := scope3.New(cfg.IdentityConfig.URL, cfg.IdentityConfig.Token, scope3.WithHTTPTimeout(cfg.IdentityConfig.Timeout))
	if err != nil {
		return nil, cleanup(fmt.Errorf("init scope3 source: %w", err))
	}
	configSvc, err := identityconfig.New(source, cfg.IdentityConfig.RefreshInterval,
		identityconfig.WithStartConfig(identityconfig.StartConfig{
			Mode: identityconfig.StartModeRetry,
			Retry: identityconfig.RetryConfig{
				Initial:  time.Second,
				Max:      30 * time.Second,
				Backoff:  identityconfig.BackoffExponential,
				Deadline: 5 * time.Minute,
			},
		}),
		identityconfig.WithLogger(logger),
	)
	if err != nil {
		return nil, cleanup(fmt.Errorf("init identityconfig service: %w", err))
	}
	if err := configSvc.Start(ctx); err != nil {
		return nil, cleanup(fmt.Errorf("identityconfig initial load: %w", err))
	}

	// Fcap valkey (required).
	fcapStore, fcapCloser, err := redisstore.Build(ctx, cfg.FCapValkey.ToRedisStoreConfig())
	if err != nil {
		configSvc.Stop()
		return nil, cleanup(fmt.Errorf("fcap valkey: %w", err))
	}
	fcapSvc := fcap.New(fcapStore)

	// Audience valkey (optional).
	var (
		audienceSvc    *audience.Service
		audienceCloser interface{ Close() error }
	)
	if cfg.AudienceValkey.Enabled {
		store, closer, err := redisstore.Build(ctx, cfg.AudienceValkey.ToRedisStoreConfig())
		if err != nil {
			_ = fcapCloser.Close()
			configSvc.Stop()
			return nil, cleanup(fmt.Errorf("audience valkey: %w", err))
		}
		audienceSvc = audience.New(store)
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
	})
	if err != nil {
		if audienceCloser != nil {
			_ = audienceCloser.Close()
		}
		_ = fcapCloser.Close()
		configSvc.Stop()
		return nil, cleanup(fmt.Errorf("build service: %w", err))
	}

	keystore, err := buildKeyStore(bgCtx, cfg.TMP.RegistryURL, !cfg.TMP.AllowUnsigned, logger)
	if err != nil {
		if audienceCloser != nil {
			_ = audienceCloser.Close()
		}
		_ = fcapCloser.Close()
		configSvc.Stop()
		return nil, cleanup(fmt.Errorf("keystore: %w", err))
	}

	tmpx, err := loadTmpxConfig(bgCtx, cfg.TMPX, logger)
	if err != nil {
		if audienceCloser != nil {
			_ = audienceCloser.Close()
		}
		_ = fcapCloser.Close()
		configSvc.Stop()
		return nil, cleanup(fmt.Errorf("tmpx: %w", err))
	}

	return &bundle{
		service:          svc,
		configSvc:        configSvc,
		audienceSvc:      audienceSvc,
		audienceCloser:   audienceCloser,
		fcapCloser:       fcapCloser,
		keystore:         keystore,
		tmpx:             tmpx,
		cancelBackground: cancel,
		hasSegmentRules:  configServiceHasSegmentRules(configSvc),
	}, nil
}

// configServiceHasSegmentRules reports whether the current snapshot contains
// any packages with non-empty TargetSegments rules. Used to decide whether
// to emit the "audience unconfigured but rules exist" warning at startup.
func configServiceHasSegmentRules(svc *identityconfig.Service) bool {
	// identityconfig.Service has no enumeration API today; the snapshot
	// is keyed by (seller, pkg) so an O(snapshot) scan would require new
	// surface. Returning false defers the warning to the request-time
	// path where the agent already marks such packages ineligible.
	return false
}
