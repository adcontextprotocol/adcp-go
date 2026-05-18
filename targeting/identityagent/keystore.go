package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// BuildKeyStore constructs a tmproto.KeyStore from the configured registry
// URL. Returns (nil, nil) when no registry URL is set and signature
// verification is not required — callers then accept unsigned requests.
//
// runCtx governs the long-lived background refresh goroutine; cancel it
// during shutdown to drain. The synchronous initial fetch is bounded to
// 10 seconds independently.
//
// The background refresh goroutine runs under safeGo: a panic inside the
// upstream library is logged at ERROR and recorded on
// recorder.BackgroundPanic("keystore-refresh") instead of taking down the
// process. recorder may be nil for callers without observability.
func BuildKeyStore(runCtx context.Context, registryURL string, requireSignature bool, logger *slog.Logger, recorder Recorder) (tmproto.KeyStore, error) {
	if registryURL == "" {
		if requireSignature {
			return nil, errors.New("registry URL is required for signature verification; pass requireSignature=false to opt out")
		}
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
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
