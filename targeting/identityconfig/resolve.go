package identityconfig

import (
	"github.com/adcontextprotocol/adcp-go/targeting"
)

// ResolveRequest converts a (seller_agent_url, package_ids) pair as it
// arrives on the identity-match wire into the effective evaluation set the
// engine will run against.
//
// Behavior matches the wire contract on `package_ids`:
//
//   - When requestedPackageIDs is empty, the buyer evaluates eligibility
//     against the full active set registered for sellerAgentURL.
//   - When requestedPackageIDs is non-empty, the buyer evaluates against the
//     intersection of its registered active set and the requested IDs.
//     Requested IDs the buyer has not registered for this seller are
//     silently ignored — surfacing them would leak registry membership.
//
// Returns the effective package IDs (in stable order) and the per-package
// PackageIdentityConfig map suitable for ResolvedPackages.IdentityConfigs.
func ResolveRequest(svc *Service, sellerAgentURL string, requestedPackageIDs []string) ([]string, map[string]*targeting.PackageIdentityConfig) {
	if svc == nil {
		return nil, nil
	}
	entries := svc.GetBySeller(sellerAgentURL)
	if len(entries) == 0 {
		return nil, nil
	}

	configs := make(map[string]*targeting.PackageIdentityConfig, len(entries))
	for _, e := range entries {
		configs[e.Key.PackageID] = &targeting.PackageIdentityConfig{TargetSegments: e.TargetSegments}
	}

	if len(requestedPackageIDs) == 0 {
		effective := make([]string, 0, len(entries))
		for _, e := range entries {
			effective = append(effective, e.Key.PackageID)
		}
		return effective, configs
	}

	effective := make([]string, 0, len(requestedPackageIDs))
	filtered := make(map[string]*targeting.PackageIdentityConfig, len(requestedPackageIDs))
	for _, id := range requestedPackageIDs {
		if cfg, ok := configs[id]; ok {
			effective = append(effective, id)
			filtered[id] = cfg
		}
	}
	return effective, filtered
}
