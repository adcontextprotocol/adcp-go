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
// Returns the effective package IDs and the per-package
// PackageIdentityConfig map suitable for ResolvedPackages.IdentityConfigs.
func ResolveRequest(svc *Service, sellerAgentURL string, requestedPackageIDs []string) ([]string, map[string]*targeting.PackageIdentityConfig) {
	if svc == nil {
		return nil, nil
	}

	if len(requestedPackageIDs) == 0 {
		entries := svc.GetBySeller(sellerAgentURL)
		if len(entries) == 0 {
			return nil, nil
		}
		effective := make([]string, 0, len(entries))
		configs := make(map[string]*targeting.PackageIdentityConfig, len(entries))
		for _, e := range entries {
			effective = append(effective, e.Key.PackageID)
			configs[e.Key.PackageID] = &targeting.PackageIdentityConfig{TargetSegments: e.TargetSegments}
		}
		return effective, configs
	}

	// Filter path: look up each requested ID directly. Avoids materializing
	// the full seller set when the caller only asks for a few packages.
	effective := make([]string, 0, len(requestedPackageIDs))
	configs := make(map[string]*targeting.PackageIdentityConfig, len(requestedPackageIDs))
	for _, id := range requestedPackageIDs {
		rule, ok := svc.Lookup(sellerAgentURL, id)
		if !ok {
			continue
		}
		effective = append(effective, id)
		configs[id] = &targeting.PackageIdentityConfig{TargetSegments: rule}
	}
	if len(effective) == 0 {
		return nil, nil
	}
	return effective, configs
}
