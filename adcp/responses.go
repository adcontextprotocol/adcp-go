package adcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Seller responses ---

// CapabilitiesResponse builds a get_adcp_capabilities response.
func CapabilitiesResponse(data *CapabilitiesData) (*mcp.CallToolResult, any, error) {
	return buildResult("Agent capabilities retrieved", data), data, nil
}

// ProductsResponse builds a get_products response.
func ProductsResponse(data *ProductsData) (*mcp.CallToolResult, any, error) {
	return buildResult(fmt.Sprintf("Found %d products", len(data.Products)), data), data, nil
}

// MediaBuyResponse builds a create_media_buy response.
func MediaBuyResponse(data *MediaBuyData) (*mcp.CallToolResult, any, error) {
	return buildResult(fmt.Sprintf("Media buy %s created", data.MediaBuyID), data), data, nil
}

// MediaBuysResponse builds a get_media_buys response.
func MediaBuysResponse(mediaBuys []MediaBuyData, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"media_buys": mediaBuys, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Found %d media buys", len(mediaBuys)), out), out, nil
}

// DeliveryResponse builds a get_media_buy_delivery response.
func DeliveryResponse(data *DeliveryData) (*mcp.CallToolResult, any, error) {
	return buildResult(fmt.Sprintf("Delivery data for %d media buys", len(data.MediaBuyDeliveries)), data), data, nil
}

// SyncAccountsResponse builds a sync_accounts response.
func SyncAccountsResponse(accounts []AccountResult, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"accounts": accounts, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Synced %d accounts", len(accounts)), out), out, nil
}

// GovernanceResponse builds a sync_governance response.
func GovernanceResponse(accounts []GovernanceResult) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"accounts": accounts}
	return buildResult("Governance synced", out), out, nil
}

// --- Creative responses ---

// SyncCreativesResponse builds a sync_creatives response.
func SyncCreativesResponse(creatives []CreativeResult, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"creatives": creatives, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Synced %d creatives", len(creatives)), out), out, nil
}

// ListCreativesResponse builds a list_creatives response with required
// query_summary and pagination fields.
func ListCreativesResponse(creatives []map[string]any) (*mcp.CallToolResult, any, error) {
	n := len(creatives)
	out := map[string]any{
		"query_summary": map[string]any{
			"total_matching": n,
			"returned":       n,
		},
		"pagination": map[string]any{
			"has_more":    false,
			"total_count": n,
		},
		"creatives": creatives,
	}
	return buildResult(fmt.Sprintf("Found %d creatives", n), out), out, nil
}

// PreviewCreativeResponse builds a preview_creative response for a single creative.
func PreviewCreativeResponse(creativeID, name, previewURL string, width, height int) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"response_type": "single",
		"previews": []map[string]any{{
			"preview_id": "preview-" + creativeID,
			"input":      map[string]any{"name": name},
			"renders": []map[string]any{{
				"render_id":     "render-" + creativeID,
				"output_format": "url",
				"preview_url":   previewURL,
				"role":          "primary",
				"dimensions":    map[string]any{"width": width, "height": height},
			}},
		}},
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}
	return buildResult("Preview generated", out), out, nil
}

// BuildCreativeResponse builds a build_creative response.
func BuildCreativeResponse(manifest map[string]any, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"creative_manifest": manifest, "sandbox": sandbox}
	return buildResult("Creative built", out), out, nil
}

// CreativeFormatsResponse builds a list_creative_formats response.
func CreativeFormatsResponse(formats []CreativeFormat, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"formats": formats, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Found %d formats", len(formats)), out), out, nil
}

// --- Signals responses ---

// SignalsResponse builds a get_signals response.
func SignalsResponse(signals []Signal, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"signals": signals, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Found %d signals", len(signals)), out), out, nil
}

// ActivateSignalResponse builds an activate_signal response.
func ActivateSignalResponse(deployments []Deployment, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"deployments": deployments, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Activated %d deployments", len(deployments)), out), out, nil
}

// --- Retail media responses ---

// SyncCatalogsResponse builds a sync_catalogs response.
func SyncCatalogsResponse(catalogs []CatalogResult, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"catalogs": catalogs, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Synced %d catalogs", len(catalogs)), out), out, nil
}

// SyncEventSourcesResponse builds a sync_event_sources response.
func SyncEventSourcesResponse(sources []EventSourceResult, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"event_sources": sources, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Synced %d event sources", len(sources)), out), out, nil
}

// LogEventResponse builds a log_event response.
func LogEventResponse(received, processed int, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"events_received": received, "events_processed": processed, "sandbox": sandbox}
	return buildResult(fmt.Sprintf("Logged %d events", received), out), out, nil
}

// PerformanceFeedbackResponse builds a provide_performance_feedback response.
func PerformanceFeedbackResponse(sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{"success": true, "sandbox": sandbox}
	return buildResult("Feedback received", out), out, nil
}

// --- Generic ---

// Result builds a generic tool response with StructuredContent.
// Returns 3 values for use with adcp.AddTool handlers.
func Result(data any, summary string) (*mcp.CallToolResult, any, error) {
	if summary == "" {
		summary = "Task completed"
	}
	return buildResult(summary, data), data, nil
}

func buildResult(summary string, data any) *mcp.CallToolResult {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte(`{}`)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: summary + "\n" + string(b)},
		},
		StructuredContent: jsonRoundTrip(data),
	}
}
