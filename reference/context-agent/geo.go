package main

import (
	"context"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// GeoFilterModule filters by geographic targeting. Package specifies
// allowed and/or blocked geo codes in Valkey sets.
type GeoFilterModule struct {
	valkey ValkeyClient
}

func NewGeoFilterModule(valkey ValkeyClient) *GeoFilterModule {
	return &GeoFilterModule{valkey: valkey}
}

func (m *GeoFilterModule) Name() string { return "geo_filter" }

func (m *GeoFilterModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))

	// Extract geo code from request
	geoCode := ""
	if req.Geo != nil {
		geoCode = req.Geo.Country
	}

	for _, pkg := range packages {
		activate := true

		if geoCode != "" {
			// Check block list
			blocked, _ := m.valkey.SIsMember(ctx, "geo:block:"+pkg.PackageID, geoCode)
			if blocked {
				activate = false
			}

			// Check allow list (if it exists, geo must be in it)
			if activate {
				allowExists, _ := m.valkey.Exists(ctx, "geo:allow:"+pkg.PackageID)
				if allowExists {
					allowed, _ := m.valkey.SIsMember(ctx, "geo:allow:"+pkg.PackageID, geoCode)
					if !allowed {
						activate = false
					}
				}
			}
		}

		score := float32(0)
		if activate {
			score = 1.0
		}
		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}
