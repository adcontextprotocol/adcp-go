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
func BuildKeyStore(runCtx context.Context, registryURL string, requireSignature bool, logger *slog.Logger) (tmproto.KeyStore, error) {
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
	go func() {
		if err := ks.Run(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("registry keystore Run terminated", "url", registryURL, "error", err)
		}
	}()
	return ks, nil
}
