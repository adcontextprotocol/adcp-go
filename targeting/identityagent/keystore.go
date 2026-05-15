package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// buildKeyStore constructs a tmproto.KeyStore from the configured registry
// URL. Returns (nil, nil) when no registry URL is set and signature
// verification is not required — the agent then accepts unsigned requests.
//
// runCtx governs the long-lived background refresh goroutine; cancel it
// during shutdown to drain the goroutine. The synchronous initial fetch is
// bounded to 10 seconds independently.
func buildKeyStore(runCtx context.Context, registryURL string, requireSignature bool, logger *slog.Logger) (tmproto.KeyStore, error) {
	if registryURL == "" {
		if requireSignature {
			return nil, errors.New("TMP_REGISTRY_URL is required for signature verification (default); set TMP_ALLOW_UNSIGNED=true to opt out")
		}
		return nil, nil
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
	go func() {
		if err := ks.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("registry keystore Run terminated", "url", registryURL, "error", err)
		}
	}()
	return ks, nil
}
