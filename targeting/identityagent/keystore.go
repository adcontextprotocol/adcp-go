package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// BuildKeyStore constructs a tmproto.KeyStore from TMPConfig. Returns
// (nil, nil) when no registry URL is set and signature verification is
// not required — callers then accept unsigned requests.
//
// runCtx governs long-lived background work (snapshot refreshes) so
// callers can cancel on shutdown; the synchronous initial fetch is
// bounded to 10 seconds independently.
//
// The background refresh goroutine runs under safeGo: a panic inside the
// upstream library is logged at ERROR and recorded on
// recorder.BackgroundPanic("keystore-refresh") instead of taking down the
// process. recorder may be nil for callers without observability.
func BuildKeyStore(runCtx context.Context, cfg TMPConfig, logger *slog.Logger, recorder Recorder) (tmproto.KeyStore, error) {
	requireSignature := !cfg.AllowUnsigned
	if cfg.RegistryURL == "" {
		if requireSignature {
			return nil, errors.New("TMP_REGISTRY_URL is required for signature verification; set TMP_ALLOW_UNSIGNED=true to opt out")
		}
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	mode := cfg.RegistryMode
	if mode == "" {
		mode = RegistryModeSnapshot
	}
	switch mode {
	case RegistryModeAuthorization:
		return buildAuthorizationKeyStore(cfg, logger)
	case RegistryModeSnapshot:
		return buildSnapshotKeyStore(runCtx, cfg.RegistryURL, logger, recorder)
	default:
		return nil, fmt.Errorf("unknown TMP_REGISTRY_MODE %q; expected %q or %q", mode, RegistryModeSnapshot, RegistryModeAuthorization)
	}
}

// buildSnapshotKeyStore polls a bulk snapshot URL for signing keys. Used
// when the verifier's caller set is known and bounded — typically when
// pointing at a router's /registry/snapshot.
func buildSnapshotKeyStore(runCtx context.Context, registryURL string, logger *slog.Logger, recorder Recorder) (tmproto.KeyStore, error) {
	ks, err := tmproto.NewRemoteKeyStore(tmproto.RemoteKeyStoreOptions{URL: registryURL})
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	defer cancel()
	if _, err := ks.Refresh(fetchCtx); err != nil {
		return nil, fmt.Errorf("initial registry fetch from %s: %w", registryURL, err)
	}
	safeGo(logger, recorder, "keystore-refresh", func() {
		// A non-Canceled return from Run means the keystore has stopped
		// refreshing. The agent will keep serving with a frozen
		// registry until the keys age out — surface at ERROR so an
		// alert fires.
		if err := ks.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("registry keystore Run terminated", "url", registryURL, "error", err)
		}
	})
	return ks, nil
}

// buildAuthorizationKeyStore instantiates the lazy per-agent keystore.
// No initial fetch — the first request from an agent warms its cache
// entry. Fits deployments where the verifier's caller set is not known
// ahead of time (e.g. the identity/context agents behind the AdCP
// property registry).
func buildAuthorizationKeyStore(cfg TMPConfig, logger *slog.Logger) (tmproto.KeyStore, error) {
	ks, err := tmproto.NewLazyAuthorizationKeyStore(tmproto.LazyAuthorizationKeyStoreOptions{
		BaseURL:     cfg.RegistryURL,
		BearerToken: cfg.RegistryBearer,
		Logger:      logger,
	})
	if err != nil {
		return nil, err
	}
	return ks, nil
}
