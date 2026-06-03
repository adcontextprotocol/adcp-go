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

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/netutil"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// Run executes the agent lifecycle: build dependencies, start the
// HTTP server, then block until SIGINT/SIGTERM and run an orderly
// shutdown. Returns non-nil only when startup fails or shutdown
// surfaces errors.
func Run(ctx context.Context, cfg Config, logger *slog.Logger, version string) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	registry := prometheus.NewRegistry()

	bundle, err := buildBundle(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("build bundle: %w", err)
	}

	contextHandler := NewHandler(HandlerConfig{
		Engine:                     bundle.engine,
		RequestTimeout:             cfg.RequestTimeout,
		RequestBodyLimit:           int64(cfg.RequestBodyLimitBytes),
		ResponseTTL:                cfg.ResponseTTL,
		SupportedADCPMajorVersions: cfg.SupportedADCPMajorVersions,
		Logger:                     logger,
	})

	requireSig := !cfg.TMP.AllowUnsigned
	if !requireSig {
		logger.Warn("/context accepts unsigned requests — TMP signing should be required in production")
	}
	if requireSig && cfg.TMP.OwnEndpointURL == "" {
		return errors.New("TMP_OWN_ENDPOINT_URL is required when signature verification is enabled")
	}

	var running atomic.Bool
	srv := NewServer(ServerConfig{
		Port:              cfg.HTTPPort,
		ContextHandler:    contextHandler,
		KeyStore:          bundle.keystore,
		OwnEndpointURL:    cfg.TMP.OwnEndpointURL,
		RequireSig:        requireSig,
		Registry:          registry,
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
		Logger:            logger,
	})

	var adminSrv *http.Server
	if cfg.AdminPort > 0 {
		adminSrv = NewAdminServer(AdminServerConfig{
			Port:              cfg.AdminPort,
			Registry:          registry,
			Version:           version,
			IsRunning:         running.Load,
			PprofEnabled:      cfg.Pprof.Enabled,
			ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
			ReadTimeout:       cfg.HTTPReadTimeout,
			WriteTimeout:      cfg.HTTPWriteTimeout,
			IdleTimeout:       cfg.HTTPIdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			Logger:            logger,
		})
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	baseLn, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	ln := netutil.LimitListener(baseLn, cfg.MaxOpenConnections)

	serverErr := make(chan error, 2)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	if adminSrv != nil {
		go func() {
			if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		}()
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

	return errors.Join(startupErr, errors.Join(shutdownErrs...))
}

// bundle holds every dependency built by buildBundle so Run can manage
// their lifetimes.
type bundle struct {
	engine           *targeting.ContextEngine
	keystore         tmproto.KeyStore
	valkeyCloser     interface{ Close() error }
	cancelBackground context.CancelFunc
}

func buildBundle(ctx context.Context, cfg Config, logger *slog.Logger) (*bundle, error) {
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
		valkeyCloser.Close()
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
		valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("suppression initial load: %w", err)
	}

	storage, err := buildStorage(
		rawStore,
		rawStore,
		rawStore,
		rawStore,
		suppressSnap,
		cfg.AcceptedTaxonomies,
		cfg.Cache,
	)
	if err != nil {
		valkeyCloser.Close()
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
	})

	keystore, err := buildKeyStore(bgCtx, cfg.TMP, logger)
	if err != nil {
		valkeyCloser.Close()
		cancelBg()
		return nil, fmt.Errorf("keystore: %w", err)
	}

	return &bundle{
		engine:           engine,
		keystore:         keystore,
		valkeyCloser:     valkeyCloser,
		cancelBackground: cancelBg,
	}, nil
}

// buildKeyStore wires up the TMP signature key store from
// TMPConfig.RegistryURL.
func buildKeyStore(ctx context.Context, cfg TMPConfig, logger *slog.Logger) (tmproto.KeyStore, error) {
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
		return nil, fmt.Errorf("initial registry fetch: %w", err)
	}
	go func() {
		if err := ks.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("registry keystore Run terminated", "error", err)
		}
	}()
	return ks, nil
}
