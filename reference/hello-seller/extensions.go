package main

// Extension tier: wire these handlers when your backend actually supports
// them. Each is independent of the others — update_media_buy doesn't need
// sync_creatives, sync_creatives doesn't need get_media_buy_delivery, etc.
// Nothing in main.go depends on this file.
//
// update_media_buy and list_creatives are registered directly via
// adcp.AddTool rather than through adcp.Config: unlike get_products or
// create_media_buy, Config has no UpdateMediaBuy/ListCreatives fields for
// them — this is the SDK's current wiring, not an omission on this example's
// part.

import (
	"context"
	"fmt"
	"time"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addExtensionHandlers fills in the Config fields that ARE wired through
// adcp.Register: sync_creatives and get_media_buy_delivery. Called from
// main.go before adcp.Register.
func addExtensionHandlers(cfg *adcp.Config, b *backend) {
	cfg.SyncCreatives = b.syncCreatives
	cfg.GetDelivery = b.getDelivery
}

// wireExtensionTools registers update_media_buy and list_creatives, which
// adcp.Register cannot wire for you. Called from main.go after
// adcp.Register, against the same server.
func wireExtensionTools(server *mcp.Server, b *backend) {
	adcp.AddTool(server, "update_media_buy", "Update a media buy",
		func(_ context.Context, _ *mcp.CallToolRequest, input adcp.UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
			return b.updateMediaBuy(input)
		})

	adcp.AddTool(server, "list_creatives", "List synced creatives",
		func(_ context.Context, _ *mcp.CallToolRequest, input adcp.ListCreativesRequest) (*mcp.CallToolResult, any, error) {
			return b.listCreatives(input)
		})
}

// syncCreatives implements sync_creatives. SWAP: validate and persist real
// creative assets in your creative-management system instead of this
// in-memory map.
func (b *backend) syncCreatives(_ context.Context, input *adcp.SyncCreativesRequest) ([]adcp.CreativeResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	results := make([]adcp.CreativeResult, 0, len(input.Creatives))
	for _, c := range input.Creatives {
		action := "created"
		if _, exists := b.creatives[c.CreativeID]; exists {
			action = "updated"
		}
		b.creatives[c.CreativeID] = &adcp.CreativeListItem{CreativeID: c.CreativeID, Name: c.Name, Status: "approved"}
		if c.FormatID != nil {
			b.creatives[c.CreativeID].FormatID = *c.FormatID
		}
		results = append(results, adcp.CreativeResult{CreativeID: c.CreativeID, Action: action, Status: "approved"})
	}
	return results, nil
}

// listCreatives implements list_creatives. SWAP: page this against your
// creative-management system for a real catalog; this demo returns
// everything synced so far, unfiltered.
func (b *backend) listCreatives(input adcp.ListCreativesRequest) (*mcp.CallToolResult, any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	creatives := make([]map[string]any, 0, len(b.creatives))
	for _, c := range b.creatives {
		item := map[string]any{
			"creative_id":  c.CreativeID,
			"name":         c.Name,
			"status":       c.Status,
			"created_date": time.Now().UTC().Format(time.RFC3339),
			"updated_date": time.Now().UTC().Format(time.RFC3339),
		}
		if c.FormatID != (adcp.FormatRef{}) {
			item["format_id"] = c.FormatID
		}
		creatives = append(creatives, item)
	}
	return adcp.ListCreativesResponse(creatives)
}

// updateMediaBuy implements update_media_buy. Only pause/resume and cancel
// are handled below — SWAP: add the package/budget/date update branches your
// backend actually supports, following the same pattern (check the
// requested transition is valid for the buy's current status, apply it,
// bump Revision, return updateMediaBuyResult).
func (b *backend) updateMediaBuy(input adcp.UpdateMediaBuyRequest) (*mcp.CallToolResult, any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	buy, ok := b.mediaBuys[input.MediaBuyID]
	if !ok {
		return adcp.Errorf("MEDIA_BUY_NOT_FOUND", adcp.ErrorOptions{Message: "Media buy not found."})
	}

	if input.Canceled != nil && *input.Canceled {
		if buy.Status == "canceled" {
			return adcp.Errorf("NOT_CANCELLABLE", adcp.ErrorOptions{Message: "Media buy is already canceled."})
		}
		buy.Status = "canceled"
	} else if input.Paused != nil {
		if *input.Paused {
			buy.Status = "paused"
		} else {
			buy.Status = "active"
		}
	}
	buy.Revision++
	buy.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	return updateMediaBuyResult(buy)
}

func updateMediaBuyResult(buy *adcp.MediaBuyData) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"media_buy_id":        buy.MediaBuyID,
		"status":              buy.Status,
		"revision":            buy.Revision,
		"implementation_date": time.Now().UTC().Format(time.RFC3339),
		// SWAP: derive this from your actual environment/config, not a
		// hardcoded literal — a real seller must be able to say "false" in
		// production. See the reference-seller README for the same gotcha.
		"sandbox": true,
	}
	return adcp.Result(out, fmt.Sprintf("Media buy %s updated", buy.MediaBuyID))
}

// getDelivery implements get_media_buy_delivery. SWAP: pull real delivery
// metrics from your ad server's reporting API — this demo always reports
// zero delivery, since hello-seller never actually serves impressions.
func (b *backend) getDelivery(_ context.Context, _ any, input *adcp.GetMediaBuyDeliveryRequest) (*adcp.DeliveryData, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now().UTC()
	ids := input.MediaBuyIDs
	if len(ids) == 0 {
		for id := range b.mediaBuys {
			ids = append(ids, id)
		}
	}
	deliveries := make([]adcp.MediaBuyDelivery, 0, len(ids))
	for _, id := range ids {
		buy, ok := b.mediaBuys[id]
		if !ok {
			continue
		}
		byPackage := make([]adcp.PackageDelivery, 0, len(buy.Packages))
		for _, pkg := range buy.Packages {
			byPackage = append(byPackage, adcp.PackageDelivery{PackageID: pkg.PackageID})
		}
		deliveries = append(deliveries, adcp.MediaBuyDelivery{
			MediaBuyID: id,
			Status:     buy.Status,
			Totals:     adcp.MediaBuyDeliveryTotals{},
			ByPackage:  byPackage,
		})
	}
	return &adcp.DeliveryData{
		ReportingPeriod:    adcp.ReportingPeriod{Start: now.Add(-24 * time.Hour).Format(time.RFC3339), End: now.Format(time.RFC3339)},
		Currency:           "USD",
		MediaBuyDeliveries: deliveries,
	}, nil
}
