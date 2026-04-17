package targeting

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/adcontextprotocol/adcp-go/exposure"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// MediaBuy represents a media buy stored in the Store.
// Store keys:
//   - "mediabuy:seller:{sellerID}" → SET of media buy IDs
//   - "mediabuy:{mediaBuyID}" → JSON MediaBuy
type MediaBuy struct {
	MediaBuyID  string            `json:"media_buy_id"`
	SellerID    string            `json:"seller_id"`
	StartDate   string            `json:"start_date"`   // "2026-01-01"
	EndDate     string            `json:"end_date"`     // "2026-12-31"
	Countries   []string          `json:"countries"`    // empty = all countries
	PropertyIDs []string          `json:"property_ids"` // empty = all seller properties
	Packages    []MediaBuyPackage `json:"packages"`
}

// MediaBuyPackage is a package within a media buy.
type MediaBuyPackage struct {
	PackageID  string   `json:"package_id"`
	MediaBuyID string   `json:"media_buy_id"`
	FormatIDs  []string `json:"format_ids,omitempty"`
}

// ResolvePackages resolves active packages for a seller by looking up media buys
// in the Store, filtering by date, geo, and property.
// The now parameter must be in UTC for correct date boundary comparison.
//
// Total: 2 Store round-trips (1 SetMembers + 1 MGet) regardless of media buy count.
func ResolvePackages(ctx context.Context, store Store, sellerID, propertyID, country string, now time.Time) ([]tmproto.AvailablePackage, error) {
	// 1. Get all media buy IDs for this seller.
	mbIDs, err := store.SetMembers(ctx, "mediabuy:seller:"+sellerID)
	if err != nil {
		return nil, fmt.Errorf("resolve media buys for seller %s: %w", sellerID, err)
	}
	if len(mbIDs) == 0 {
		return nil, nil
	}

	// 2. Batch-load all media buy JSON.
	keys := make([]string, len(mbIDs))
	for i, id := range mbIDs {
		keys[i] = "mediabuy:" + id
	}
	values, err := store.MGet(ctx, keys...)
	if err != nil {
		return nil, fmt.Errorf("load media buys for seller %s: %w", sellerID, err)
	}

	// 3. Parse, filter, collect packages.
	var result []tmproto.AvailablePackage
	for _, val := range values {
		if val == "" {
			continue
		}
		var mb MediaBuy
		if err := json.Unmarshal([]byte(val), &mb); err != nil {
			continue // skip unparseable entries
		}

		if !isActive(mb, now) {
			continue
		}
		if !matchesGeo(mb, country) {
			continue
		}
		if !matchesProperty(mb, propertyID) {
			continue
		}

		for _, pkg := range mb.Packages {
			// Convert string format IDs to json.RawMessage for the wire type.
			var fmtIDs []json.RawMessage
			for _, fid := range pkg.FormatIDs {
				b, _ := json.Marshal(fid)
				fmtIDs = append(fmtIDs, b)
			}
			result = append(result, tmproto.AvailablePackage{
				PackageID:  pkg.PackageID,
				MediaBuyID: pkg.MediaBuyID,
				FormatIDs:  fmtIDs,
			})
		}
	}

	return result, nil
}

// Resolve builds a fully-indexed ResolvedPackages for a seller+property+country.
// This is the cacheable entry point — call once, cache the result, use for
// many requests. Loads all configs and builds all indexes.
func Resolve(ctx context.Context, store Store, sellerID, propertyID, country string, now time.Time) (*ResolvedPackages, error) {
	// Step 1: Resolve active packages.
	pkgs, err := ResolvePackages(ctx, store, sellerID, propertyID, country, now)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return &ResolvedPackages{}, nil
	}

	pkgIDs := make([]string, len(pkgs))
	for i, p := range pkgs {
		pkgIDs[i] = p.PackageID
	}

	// Step 2: Batch-load context configs (1 MGet).
	ctxConfigs, err := batchLoadPackageContextConfigs(ctx, store, pkgIDs)
	if err != nil {
		ctxConfigs = make(map[string]*PackageContextConfig)
	}

	// Step 3: Batch-load identity configs (1 MGet).
	idConfigs, err := exposure.BatchLoadPackageIdentityConfigs(ctx, store, pkgIDs)
	if err != nil {
		idConfigs = make(map[string]*exposure.PackageIdentityConfig)
	}

	// Step 4: Collect unique campaign IDs, batch-load (1 MGet).
	campIDSet := make(map[string]struct{})
	for _, cfg := range idConfigs {
		if cfg != nil && cfg.CampaignID != "" {
			campIDSet[cfg.CampaignID] = struct{}{}
		}
	}
	campIDs := make([]string, 0, len(campIDSet))
	for id := range campIDSet {
		campIDs = append(campIDs, id)
	}
	campConfigs, err := exposure.BatchLoadCampaignFreqConfigs(ctx, store, campIDs)
	if err != nil {
		campConfigs = make(map[string]*exposure.CampaignFreqConfig)
	}

	// Step 5: Build indexes.
	propertyIdx := make(map[string][]string)
	topicIdx := make(map[string][]string)
	urlBlockIdx := make(map[string][]string)
	urlAllowlists := make(map[string]map[string]struct{})
	segmentIdx := make(map[string][]string)

	for _, pkgID := range pkgIDs {
		// Property index from context config.
		if cc := ctxConfigs[pkgID]; cc != nil {
			for _, rid := range cc.PropertyRIDs {
				propertyIdx[rid] = append(propertyIdx[rid], pkgID)
			}
		}

		// Topic index: load topic set from Store.
		topics, _ := store.SetMembers(ctx, "topics:package:"+pkgID)
		for _, topic := range topics {
			topicIdx[topic] = append(topicIdx[topic], pkgID)
		}

		// URL blocklist index: load blocklist from Store.
		if cc := ctxConfigs[pkgID]; cc != nil && cc.URLBlocklist {
			blocked, _ := store.SetMembers(ctx, "url:blocklist:"+pkgID)
			for _, hash := range blocked {
				urlBlockIdx[hash] = append(urlBlockIdx[hash], pkgID)
			}
		}

		// URL allowlist: load allowlist from Store.
		if cc := ctxConfigs[pkgID]; cc != nil && cc.URLAllowlist {
			allowed, _ := store.SetMembers(ctx, "url:allowlist:"+pkgID)
			if len(allowed) > 0 {
				set := make(map[string]struct{}, len(allowed))
				for _, hash := range allowed {
					set[hash] = struct{}{}
				}
				urlAllowlists[pkgID] = set
			}
		}

		// Segment index from identity config.
		if ic := idConfigs[pkgID]; ic != nil {
			for _, seg := range ic.TargetSegments {
				segmentIdx[seg] = append(segmentIdx[seg], pkgID)
			}
		}
	}

	return &ResolvedPackages{
		Packages:          pkgs,
		PropertyIndex:     propertyIdx,
		TopicIndex:        topicIdx,
		URLBlocklistIndex: urlBlockIdx,
		URLAllowlists:     urlAllowlists,
		SegmentIndex:      segmentIdx,
		ContextConfigs:    ctxConfigs,
		IdentityConfigs:   idConfigs,
		CampaignConfigs:   campConfigs,
	}, nil
}

func isActive(mb MediaBuy, now time.Time) bool {
	today := now.Truncate(24 * time.Hour)
	if mb.StartDate != "" {
		start, err := time.Parse("2006-01-02", mb.StartDate)
		if err != nil {
			return false
		}
		if today.Before(start) {
			return false
		}
	}
	if mb.EndDate != "" {
		end, err := time.Parse("2006-01-02", mb.EndDate)
		if err != nil {
			return false
		}
		if today.After(end) {
			return false
		}
	}
	return true
}

func matchesGeo(mb MediaBuy, country string) bool {
	if len(mb.Countries) == 0 {
		return true
	}
	return slices.Contains(mb.Countries, country)
}

func matchesProperty(mb MediaBuy, propertyID string) bool {
	if len(mb.PropertyIDs) == 0 {
		return true
	}
	return slices.Contains(mb.PropertyIDs, propertyID)
}
