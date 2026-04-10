package adcp

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

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
// Includes both media_buy_deliveries and media_buys keys (storyboards check both).
func DeliveryResponse(data *DeliveryData) (*mcp.CallToolResult, any, error) {
	n := len(data.MediaBuyDeliveries)
	s := "s"
	if n == 1 {
		s = ""
	}
	out := map[string]any{
		"reporting_period":     data.ReportingPeriod,
		"media_buy_deliveries": data.MediaBuyDeliveries,
		"media_buys":          data.MediaBuyDeliveries,
	}
	return buildResult(fmt.Sprintf("Delivery data for %d media buy%s", n, s), out), out, nil
}

// SyncCreativesResponse builds a sync_creatives response.
func SyncCreativesResponse(creatives []CreativeResult, sandbox bool) (*mcp.CallToolResult, any, error) {
	out := map[string]any{
		"creatives": creatives,
		"sandbox":   sandbox,
	}
	return buildResult(fmt.Sprintf("Synced %d creatives", len(creatives)), out), out, nil
}

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
