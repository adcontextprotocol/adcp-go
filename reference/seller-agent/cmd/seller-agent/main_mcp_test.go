package main

// MCP-level tests exercise the seller's tools through the real registered
// MCP server (newServer, which wires the same adcp.Register/adcp.AddTool/
// adcp.RegisterTestController calls main() uses to serve requests) over an
// in-memory transport.
//
// The unit tests in main_test.go call backend methods (b.createMediaBuy,
// b.updateMediaBuy, ...) directly, which exercises the state machine but
// bypasses tool registration and JSON request/response marshaling entirely.
// These tests instead dispatch by tool name with JSON-shaped arguments the
// way a real MCP client — including the npm storyboard runner — would, so a
// regression in which tools are registered, their names, or their request/
// response field mapping is also caught by `go test ./...` and not only by
// the storyboard.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newMCPTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return newMCPTestSessionForBackend(t, newTestBackend())
}

func newMCPTestSessionForBackend(t *testing.T, b *backend) *mcp.ClientSession {
	t.Helper()
	server := newServer(b)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx := context.Background()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "seller-agent-test-client", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	return clientSession
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call tool %s: %v", name, err)
	}
	if result == nil {
		t.Fatalf("call tool %s: nil result", name)
	}
	return result
}

func structuredMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	return m
}

// --- create_media_buy over MCP: with and without creative assignments ---

func TestMCP_CreateMediaBuy_WithoutCreatives(t *testing.T) {
	session := newMCPTestSession(t)

	result := callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{
			map[string]any{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0},
		},
	})
	if result.IsError {
		t.Fatalf("create_media_buy returned error result: %s", mustMarshal(t, result.StructuredContent))
	}
	wire := structuredMap(t, result)
	if wire["status"] != "pending_creatives" {
		t.Errorf("want status pending_creatives, got %v", wire["status"])
	}
	if id, _ := wire["media_buy_id"].(string); id == "" {
		t.Error("expected non-empty media_buy_id")
	}
}

func TestMCP_CreateMediaBuy_WithCreatives(t *testing.T) {
	session := newMCPTestSession(t)

	result := callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{
			map[string]any{
				"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0,
				"creative_assignments": []any{map[string]any{"creative_id": "cr-mcp-initial"}},
			},
		},
	})
	if result.IsError {
		t.Fatalf("create_media_buy returned error result: %s", mustMarshal(t, result.StructuredContent))
	}
	wire := structuredMap(t, result)
	if wire["status"] != "active" {
		t.Errorf("want status active when creatives supplied at create, got %v", wire["status"])
	}
}

// --- pending_creatives -> active over MCP, via sync_creatives and update_media_buy ---

func TestMCP_PendingCreativesToActive_ViaSyncCreatives(t *testing.T) {
	session := newMCPTestSession(t)

	created := structuredMap(t, callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{map[string]any{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0}},
	}))
	mediaBuyID, _ := created["media_buy_id"].(string)
	pkgID := firstPackageID(t, created)

	syncResult := callTool(t, session, "sync_creatives", map[string]any{
		"creatives":   []any{map[string]any{"creative_id": "cr-mcp-sync", "name": "Banner"}},
		"assignments": []any{map[string]any{"creative_id": "cr-mcp-sync", "package_id": pkgID}},
	})
	if syncResult.IsError {
		t.Fatalf("sync_creatives returned error result: %s", mustMarshal(t, syncResult.StructuredContent))
	}

	getResult := callTool(t, session, "get_media_buys", map[string]any{"media_buy_ids": []any{mediaBuyID}})
	if getResult.IsError {
		t.Fatalf("get_media_buys returned error result: %s", mustMarshal(t, getResult.StructuredContent))
	}
	if status := firstMediaBuyStatus(t, structuredMap(t, getResult)); status != "active" {
		t.Errorf("want active after sync_creatives with assignment, got %v", status)
	}
}

func TestMCP_PendingCreativesToActive_ViaUpdateMediaBuy(t *testing.T) {
	session := newMCPTestSession(t)

	created := structuredMap(t, callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{map[string]any{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0}},
	}))
	mediaBuyID, _ := created["media_buy_id"].(string)
	pkgID := firstPackageID(t, created)

	updateResult := callTool(t, session, "update_media_buy", map[string]any{
		"media_buy_id": mediaBuyID,
		"packages": []any{
			map[string]any{"package_id": pkgID, "creative_assignments": []any{map[string]any{"creative_id": "cr-mcp-upd"}}},
		},
	})
	if updateResult.IsError {
		t.Fatalf("update_media_buy returned error result: %s", mustMarshal(t, updateResult.StructuredContent))
	}
	wire := structuredMap(t, updateResult)
	if wire["status"] != "active" {
		t.Errorf("want active after update_media_buy with creative assignment, got %v", wire["status"])
	}
}

// --- cancellation and double-cancel over MCP ---

func TestMCP_CancelAndDoubleCancel(t *testing.T) {
	session := newMCPTestSession(t)

	created := structuredMap(t, callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{map[string]any{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0}},
	}))
	mediaBuyID, _ := created["media_buy_id"].(string)
	cancelArgs := map[string]any{"media_buy_id": mediaBuyID, "canceled": true, "cancellation_reason": "budget_cut"}

	first := callTool(t, session, "update_media_buy", cancelArgs)
	if first.IsError {
		t.Fatalf("first cancel returned error result: %s", mustMarshal(t, first.StructuredContent))
	}
	if wire := structuredMap(t, first); wire["status"] != "canceled" {
		t.Errorf("want canceled after first cancel, got %v", wire["status"])
	}

	second := callTool(t, session, "update_media_buy", cancelArgs)
	if !second.IsError {
		t.Fatal("expected error result on double-cancel via MCP, got success")
	}
	if code := resultErrorCode(t, second); code != "NOT_CANCELLABLE" {
		t.Errorf("want NOT_CANCELLABLE on double-cancel, got %q", code)
	}
}

// --- list_creatives filtering over MCP ---

func TestMCP_ListCreatives_FilterByCreativeIDAndFormatID(t *testing.T) {
	session := newMCPTestSession(t)

	fmtA := map[string]any{"agent_url": "http://test", "id": "banner-300x250"}
	fmtB := map[string]any{"agent_url": "http://test", "id": "video-15s"}

	syncResult := callTool(t, session, "sync_creatives", map[string]any{
		"creatives": []any{
			map[string]any{"creative_id": "cr-mcp-a", "format_id": fmtA},
			map[string]any{"creative_id": "cr-mcp-b", "format_id": fmtB},
		},
	})
	if syncResult.IsError {
		t.Fatalf("sync_creatives returned error result: %s", mustMarshal(t, syncResult.StructuredContent))
	}

	byID := structuredMap(t, callTool(t, session, "list_creatives", map[string]any{
		"filters": map[string]any{"creative_ids": []any{"cr-mcp-a"}},
	}))
	idItems, _ := byID["creatives"].([]any)
	if len(idItems) != 1 {
		t.Fatalf("want 1 creative filtered by creative_ids, got %d: %#v", len(idItems), idItems)
	}
	if first, ok := idItems[0].(map[string]any); !ok || first["creative_id"] != "cr-mcp-a" {
		t.Errorf("want cr-mcp-a, got %#v", idItems[0])
	}

	byFormat := structuredMap(t, callTool(t, session, "list_creatives", map[string]any{
		"filters": map[string]any{"format_ids": []any{fmtB}},
	}))
	fmtItems, _ := byFormat["creatives"].([]any)
	if len(fmtItems) != 1 {
		t.Fatalf("want 1 creative filtered by format_ids, got %d: %#v", len(fmtItems), fmtItems)
	}
	if first, ok := fmtItems[0].(map[string]any); !ok || first["creative_id"] != "cr-mcp-b" {
		t.Errorf("want cr-mcp-b, got %#v", fmtItems[0])
	}
}

// --- delivery simulation/reporting over MCP: comply_test_controller then get_media_buy_delivery ---

func TestMCP_DeliverySimulationAndReporting(t *testing.T) {
	session := newMCPTestSession(t)

	created := structuredMap(t, callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{
			map[string]any{
				"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 1000.0,
				"creative_assignments": []any{map[string]any{"creative_id": "cr-mcp-delivery"}},
			},
		},
	}))
	mediaBuyID, _ := created["media_buy_id"].(string)

	simResult := callTool(t, session, "comply_test_controller", map[string]any{
		"scenario": "simulate_delivery",
		"params": map[string]any{
			"media_buy_id":   mediaBuyID,
			"impressions":    1000.0,
			"clicks":         50.0,
			"reported_spend": map[string]any{"amount": 15.0, "currency": "USD"},
		},
	})
	if simResult.IsError {
		t.Fatalf("simulate_delivery returned error result: %s", mustMarshal(t, simResult.StructuredContent))
	}

	deliveryResult := callTool(t, session, "get_media_buy_delivery", map[string]any{"media_buy_ids": []any{mediaBuyID}})
	if deliveryResult.IsError {
		t.Fatalf("get_media_buy_delivery returned error result: %s", mustMarshal(t, deliveryResult.StructuredContent))
	}
	wire := structuredMap(t, deliveryResult)
	deliveries, _ := wire["media_buy_deliveries"].([]any)
	if len(deliveries) != 1 {
		t.Fatalf("want 1 media buy delivery, got %d", len(deliveries))
	}
	entry, ok := deliveries[0].(map[string]any)
	if !ok {
		t.Fatalf("expected delivery entry object, got %#v", deliveries[0])
	}
	totals, ok := entry["totals"].(map[string]any)
	if !ok {
		t.Fatalf("expected totals object in delivery response, got %#v", entry["totals"])
	}
	if impressions, _ := totals["impressions"].(float64); impressions != 1000 {
		t.Errorf("want 1000 impressions, got %v", totals["impressions"])
	}
	if spend, _ := totals["spend"].(float64); spend != 15 {
		t.Errorf("want 15 spend, got %v", totals["spend"])
	}
}

// --- custom comply_test_controller scenarios over MCP ---

func TestMCP_ComplyTestController_SeedProduct(t *testing.T) {
	session := newMCPTestSession(t)

	seedResult := callTool(t, session, "comply_test_controller", map[string]any{
		"scenario": "seed_product",
		"params": map[string]any{
			"product_id": "mcp-seeded-product",
			"fixture":    map[string]any{"channels": []any{"video"}, "delivery_type": "non_guaranteed"},
		},
	})
	if seedResult.IsError {
		t.Fatalf("seed_product scenario returned error result: %s", mustMarshal(t, seedResult.StructuredContent))
	}

	productsWire := structuredMap(t, callTool(t, session, "get_products", map[string]any{}))
	products, _ := productsWire["products"].([]any)
	found := false
	for _, p := range products {
		m, ok := p.(map[string]any)
		if ok && m["product_id"] == "mcp-seeded-product" {
			found = true
			if m["delivery_type"] != "non_guaranteed" {
				t.Errorf("want delivery_type non_guaranteed, got %v", m["delivery_type"])
			}
		}
	}
	if !found {
		t.Fatal("seeded product not found in get_products response")
	}
}

func TestMCP_ComplyTestController_ForceCreateMediaBuyArm(t *testing.T) {
	session := newMCPTestSession(t)

	armResult := callTool(t, session, "comply_test_controller", map[string]any{
		"scenario": "force_create_media_buy_arm",
		"params":   map[string]any{"arm": "submitted", "task_id": "mcp-task-1", "message": "queued for review"},
	})
	if armResult.IsError {
		t.Fatalf("force_create_media_buy_arm returned error result: %s", mustMarshal(t, armResult.StructuredContent))
	}

	createResult := callTool(t, session, "create_media_buy", map[string]any{
		"packages": []any{map[string]any{"product_id": "premium-display", "pricing_option_id": "pd-cpm-15", "budget": 500.0}},
	})
	if createResult.IsError {
		t.Fatalf("create_media_buy after forced arm returned error result: %s", mustMarshal(t, createResult.StructuredContent))
	}
	wire := structuredMap(t, createResult)
	if wire["status"] != "submitted" {
		t.Errorf("want submitted status after forced arm, got %v", wire["status"])
	}
	if wire["task_id"] != "mcp-task-1" {
		t.Errorf("want task_id mcp-task-1, got %v", wire["task_id"])
	}
}

func TestMCP_ComplyTestController_UnknownScenario(t *testing.T) {
	session := newMCPTestSession(t)

	result := callTool(t, session, "comply_test_controller", map[string]any{"scenario": "totally_unsupported_scenario"})
	if !result.IsError {
		t.Fatal("expected error result for unknown scenario")
	}
}

// --- helpers ---

func firstPackageID(t *testing.T, wire map[string]any) string {
	t.Helper()
	packages, ok := wire["packages"].([]any)
	if !ok || len(packages) == 0 {
		t.Fatalf("expected at least 1 package in response: %#v", wire["packages"])
	}
	pkg, ok := packages[0].(map[string]any)
	if !ok {
		t.Fatalf("expected package object, got %#v", packages[0])
	}
	id, _ := pkg["package_id"].(string)
	if id == "" {
		t.Fatal("expected non-empty package_id")
	}
	return id
}

func firstMediaBuyStatus(t *testing.T, wire map[string]any) string {
	t.Helper()
	buys, ok := wire["media_buys"].([]any)
	if !ok || len(buys) == 0 {
		t.Fatalf("expected at least 1 media buy in get_media_buys response: %#v", wire["media_buys"])
	}
	buy, ok := buys[0].(map[string]any)
	if !ok {
		t.Fatalf("expected media buy object, got %#v", buys[0])
	}
	status, _ := buy["status"].(string)
	return status
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
