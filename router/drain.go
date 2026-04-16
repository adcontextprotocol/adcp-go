package router

import (
	"context"
	"fmt"
	"time"
)

// DrainProvider sets a provider to draining status and waits for in-flight
// requests to complete. Once drained, the provider is set to inactive.
// Respects context cancellation — if the context expires, the provider is
// forcibly set to inactive.
func (r *Router) DrainProvider(ctx context.Context, providerID string) error {
	if !r.providers.SetStatus(providerID, ProviderStatusDraining) {
		return fmt.Errorf("provider %q not found", providerID)
	}

	if r.health == nil {
		r.providers.SetStatus(providerID, ProviderStatusInactive)
		return nil
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if r.health.Inflight(providerID) == 0 {
			r.providers.SetStatus(providerID, ProviderStatusInactive)
			return nil
		}
		select {
		case <-ctx.Done():
			r.providers.SetStatus(providerID, ProviderStatusInactive)
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
