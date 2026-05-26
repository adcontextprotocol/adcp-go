package main

import (
	"encoding/json"
	"testing"

	"github.com/adcontextprotocol/adcp-go/adcp"
)

func newTestBackend() *backend {
	return &backend{
		accounts:  make(map[string]*adcp.AccountResult),
		products:  baseProducts(),
		mediaBuys: make(map[string]*adcp.MediaBuyData),
		creatives: make(map[string]*creativeRecord),
		delivery:  make(map[string]*deliveryState),
	}
}

func mustCreateBuy(t *testing.T, b *backend, withCreatives bool) *adcp.MediaBuyData {
	t.Helper()
	pkg := adcp.PackageInput{ProductID: "premium-display", PricingOptionID: "pd-cpm-15", Budget: 1000}
	if withCreatives {
		pkg.CreativeAssignments = []adcp.CreativeAssignment{{CreativeID: "cr-initial"}}
	}
	buy, err := b.createMediaBuy(&adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{pkg},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}
	return buy
}

// --- create_media_buy ---

func TestCreateMediaBuy_WithoutCreatives(t *testing.T) {
	b := newTestBackend()
	buy, err := b.createMediaBuy(&adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: "premium-display", Budget: 500}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buy.Status != "pending_creatives" {
		t.Errorf("want status pending_creatives, got %s", buy.Status)
	}
	if buy.MediaBuyID == "" {
		t.Error("expected non-empty MediaBuyID")
	}
	if len(buy.Packages) != 1 {
		t.Errorf("want 1 package, got %d", len(buy.Packages))
	}
}

func TestCreateMediaBuy_WithCreatives(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, true)
	if buy.Status != "active" {
		t.Errorf("want status active when creatives supplied at create, got %s", buy.Status)
	}
}

func TestCreateMediaBuy_IDsAreSequential(t *testing.T) {
	b := newTestBackend()
	a := mustCreateBuy(t, b, false)
	c := mustCreateBuy(t, b, false)
	if a.MediaBuyID == c.MediaBuyID {
		t.Errorf("expected distinct IDs, both got %s", a.MediaBuyID)
	}
}

func TestCreateMediaBuy_ScrubsWriteOnlyInvoiceRecipientBank(t *testing.T) {
	b := newTestBackend()
	buy, err := b.createMediaBuy(&adcp.CreateMediaBuyRequest{
		InvoiceRecipient: &adcp.BusinessEntity{
			LegalName: "Acme Corporation",
			Bank:      &adcp.BankAccount{AccountHolder: "Acme Corporation", AccountNumber: "123456789"},
		},
		Packages: []adcp.PackageInput{{ProductID: "premium-display", Budget: 500}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}
	if buy.InvoiceRecipient == nil {
		t.Fatal("expected invoice recipient")
	}
	if buy.InvoiceRecipient.Bank != nil {
		t.Fatalf("write-only bank details should not be stored: %#v", buy.InvoiceRecipient.Bank)
	}

	response := mediaBuyCreateSuccess(buy)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal create success: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal create success: %v", err)
	}
	recipient, ok := wire["invoice_recipient"].(map[string]any)
	if !ok {
		t.Fatalf("expected invoice_recipient object, got %#v", wire["invoice_recipient"])
	}
	if _, ok := recipient["bank"]; ok {
		t.Fatalf("write-only bank details should not be echoed: %s", raw)
	}
}

// --- pending_creatives → active state transitions ---

func TestPendingCreativesToActive_ViaSyncCreatives(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)
	if buy.Status != "pending_creatives" {
		t.Fatalf("pre-condition: want pending_creatives, got %s", buy.Status)
	}
	pkgID := buy.Packages[0].PackageID
	revisionBefore := buy.Revision

	_, err := b.syncCreatives(&adcp.SyncCreativesRequest{
		Creatives:   []adcp.CreativeInput{{CreativeID: "cr-sync-1", Name: "Banner"}},
		Assignments: []adcp.SyncCreativeAssignment{{CreativeID: "cr-sync-1", PackageID: pkgID}},
	})
	if err != nil {
		t.Fatalf("syncCreatives: %v", err)
	}

	updated := b.mediaBuys[buy.MediaBuyID]
	if updated.Status != "active" {
		t.Errorf("want active after sync_creatives with assignment, got %s", updated.Status)
	}
	if updated.Revision <= revisionBefore {
		t.Errorf("want revision to increment on status change, before=%d after=%d", revisionBefore, updated.Revision)
	}
}

func TestPendingCreativesToActive_ViaUpdateMediaBuy(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)
	pkgID := buy.Packages[0].PackageID

	result, _, err := b.updateMediaBuy(adcp.UpdateMediaBuyRequest{
		MediaBuyID: buy.MediaBuyID,
		Packages: []adcp.PackageUpdate{{
			PackageID:           pkgID,
			CreativeAssignments: []adcp.CreativeAssignment{{CreativeID: "cr-upd-1"}},
		}},
	})
	if err != nil {
		t.Fatalf("updateMediaBuy: %v", err)
	}
	if result.IsError {
		t.Fatal("updateMediaBuy returned error result")
	}

	updated := b.mediaBuys[buy.MediaBuyID]
	if updated.Status != "active" {
		t.Errorf("want active after update_media_buy with creative assignment, got %s", updated.Status)
	}
}

// --- cancellation ---

func TestCancellation(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)
	canceled := true

	result, _, err := b.updateMediaBuy(adcp.UpdateMediaBuyRequest{
		MediaBuyID:         buy.MediaBuyID,
		Canceled:           &canceled,
		CancellationReason: "budget_cut",
	})
	if err != nil {
		t.Fatalf("updateMediaBuy cancel: %v", err)
	}
	if result.IsError {
		t.Fatal("cancel returned error result")
	}

	updated := b.mediaBuys[buy.MediaBuyID]
	if updated.Status != "canceled" {
		t.Errorf("want canceled, got %s", updated.Status)
	}
	if updated.Cancellation == nil {
		t.Error("expected Cancellation metadata to be set")
	}
}

func TestDoubleCancellation(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)
	canceled := true

	// First cancel succeeds.
	_, _, _ = b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: buy.MediaBuyID, Canceled: &canceled})

	// Second cancel must return an error result (NOT_CANCELLABLE), not a hard error.
	result, _, err := b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: buy.MediaBuyID, Canceled: &canceled})
	if err != nil {
		t.Fatalf("second cancel: unexpected hard error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result on double-cancel")
	}
}

func TestUpdateMediaBuy_UnknownStatusRejectsStateChanges(t *testing.T) {
	cases := []struct {
		name  string
		input func(buy *adcp.MediaBuyData) adcp.UpdateMediaBuyRequest
	}{
		{
			name: "cancel",
			input: func(buy *adcp.MediaBuyData) adcp.UpdateMediaBuyRequest {
				canceled := true
				return adcp.UpdateMediaBuyRequest{MediaBuyID: buy.MediaBuyID, Canceled: &canceled}
			},
		},
		{
			name: "pause",
			input: func(buy *adcp.MediaBuyData) adcp.UpdateMediaBuyRequest {
				paused := true
				return adcp.UpdateMediaBuyRequest{MediaBuyID: buy.MediaBuyID, Paused: &paused}
			},
		},
		{
			name: "package update",
			input: func(buy *adcp.MediaBuyData) adcp.UpdateMediaBuyRequest {
				paused := true
				return adcp.UpdateMediaBuyRequest{
					MediaBuyID: buy.MediaBuyID,
					Packages: []adcp.PackageUpdate{{
						PackageID: buy.Packages[0].PackageID,
						Paused:    &paused,
					}},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBackend()
			buy := mustCreateBuy(t, b, true)
			buy.Status = "future_status"

			result, _, err := b.updateMediaBuy(tc.input(buy))
			if err != nil {
				t.Fatalf("updateMediaBuy: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected error result for unknown status transition")
			}
			if buy.Status != "future_status" {
				t.Fatalf("unknown status transition changed status to %s", buy.Status)
			}
		})
	}
}

// --- list_creatives filtering ---

func TestListCreatives_FilterByCreativeID(t *testing.T) {
	b := newTestBackend()
	_, _ = b.syncCreatives(&adcp.SyncCreativesRequest{
		Creatives: []adcp.CreativeInput{
			{CreativeID: "cr-a", Name: "Alpha"},
			{CreativeID: "cr-b", Name: "Beta"},
		},
	})

	_, out, err := b.listCreatives(adcp.ListCreativesRequest{
		Filters: &adcp.CreativeFilters{CreativeIDs: []string{"cr-a"}},
	})
	if err != nil {
		t.Fatalf("listCreatives: %v", err)
	}
	m := out.(map[string]any)
	items := m["creatives"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("want 1 creative, got %d", len(items))
	}
	if items[0]["creative_id"] != "cr-a" {
		t.Errorf("want cr-a, got %v", items[0]["creative_id"])
	}
}

func TestListCreatives_FilterByFormatID(t *testing.T) {
	b := newTestBackend()
	fmtA := &adcp.FormatRef{AgentURL: "http://test", ID: "banner-300x250"}
	fmtB := &adcp.FormatRef{AgentURL: "http://test", ID: "video-15s"}
	fmtOtherAgent := &adcp.FormatRef{AgentURL: "http://other-agent", ID: "banner-300x250"}
	_, _ = b.syncCreatives(&adcp.SyncCreativesRequest{
		Creatives: []adcp.CreativeInput{
			{CreativeID: "cr-fmt-a", FormatID: fmtA},
			{CreativeID: "cr-fmt-b", FormatID: fmtB},
			{CreativeID: "cr-fmt-other-agent", FormatID: fmtOtherAgent},
		},
	})

	_, out, err := b.listCreatives(adcp.ListCreativesRequest{
		Filters: &adcp.CreativeFilters{
			FormatIDs: []adcp.FormatRef{{AgentURL: "http://test", ID: "banner-300x250"}},
		},
	})
	if err != nil {
		t.Fatalf("listCreatives by format: %v", err)
	}
	m := out.(map[string]any)
	items := m["creatives"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("want 1 creative filtered by format, got %d", len(items))
	}
	if items[0]["creative_id"] != "cr-fmt-a" {
		t.Errorf("want cr-fmt-a, got %v", items[0]["creative_id"])
	}
}

func TestListCreatives_NoFilter_ReturnsAll(t *testing.T) {
	b := newTestBackend()
	_, _ = b.syncCreatives(&adcp.SyncCreativesRequest{
		Creatives: []adcp.CreativeInput{
			{CreativeID: "cr-1"},
			{CreativeID: "cr-2"},
			{CreativeID: "cr-3"},
		},
	})

	_, out, err := b.listCreatives(adcp.ListCreativesRequest{})
	if err != nil {
		t.Fatalf("listCreatives: %v", err)
	}
	m := out.(map[string]any)
	items := m["creatives"].([]map[string]any)
	if len(items) != 3 {
		t.Errorf("want 3 creatives, got %d", len(items))
	}
}

// --- delivery reporting ---

func TestDeliveryReporting_SimulateDelivery(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, true)

	_, err := b.simulateDelivery(buy.MediaBuyID, adcp.SimulateDeliveryParams{
		Impressions:   1000,
		Clicks:        50,
		ReportedSpend: &adcp.ReportedSpend{Amount: 15.00, Currency: "USD"},
	})
	if err != nil {
		t.Fatalf("simulateDelivery: %v", err)
	}

	data, err := b.getDelivery(&adcp.GetMediaBuyDeliveryRequest{
		MediaBuyIDs: []string{buy.MediaBuyID},
	})
	if err != nil {
		t.Fatalf("getDelivery: %v", err)
	}
	if len(data.MediaBuyDeliveries) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(data.MediaBuyDeliveries))
	}
	totals := data.MediaBuyDeliveries[0].Totals
	if totals.Impressions != 1000 {
		t.Errorf("want 1000 impressions, got %.0f", totals.Impressions)
	}
	if totals.Clicks != 50 {
		t.Errorf("want 50 clicks, got %.0f", totals.Clicks)
	}
	if totals.Spend != 15.00 {
		t.Errorf("want 15.00 spend, got %.2f", totals.Spend)
	}
	pkg := data.MediaBuyDeliveries[0].ByPackage[0]
	if pkg.Impressions != 1000 || pkg.Clicks != 50 || pkg.Spend != 15.00 {
		t.Errorf("want flat package metrics, got impressions=%.0f clicks=%.0f spend=%.2f", pkg.Impressions, pkg.Clicks, pkg.Spend)
	}
	wire, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal package delivery: %v", err)
	}
	var row map[string]any
	if err := json.Unmarshal(wire, &row); err != nil {
		t.Fatalf("unmarshal package delivery: %v", err)
	}
	if _, ok := row["totals"]; ok {
		t.Error("package delivery row should use flat delivery metrics, not nested totals")
	}
}

func TestDeliveryReporting_SimulateBudgetSpend(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, true)

	_, err := b.simulateBudgetSpend(adcp.SimulateBudgetParams{
		MediaBuyID:      buy.MediaBuyID,
		SpendPercentage: 0.5,
	})
	if err != nil {
		t.Fatalf("simulateBudgetSpend: %v", err)
	}

	data, err := b.getDelivery(&adcp.GetMediaBuyDeliveryRequest{
		MediaBuyIDs: []string{buy.MediaBuyID},
	})
	if err != nil {
		t.Fatalf("getDelivery: %v", err)
	}
	totals := data.MediaBuyDeliveries[0].Totals
	want := buy.TotalBudget * 0.5
	if totals.Spend != want {
		t.Errorf("want spend %.2f (50%% of budget %.2f), got %.2f", want, buy.TotalBudget, totals.Spend)
	}
}

func TestDeliveryReporting_SimulateDeliveryWeightsSpendByBudget(t *testing.T) {
	b := newTestBackend()
	buy, err := b.createMediaBuy(&adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{
			{ProductID: "premium-display", PricingOptionID: "pd-cpm-15", Budget: 100, CreativeAssignments: []adcp.CreativeAssignment{{CreativeID: "cr-a"}}},
			{ProductID: "premium-display", PricingOptionID: "pd-cpm-15", Budget: 300, CreativeAssignments: []adcp.CreativeAssignment{{CreativeID: "cr-b"}}},
		},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}

	_, err = b.simulateDelivery(buy.MediaBuyID, adcp.SimulateDeliveryParams{
		Impressions:   100,
		Clicks:        10,
		ReportedSpend: &adcp.ReportedSpend{Amount: 40, Currency: "USD"},
	})
	if err != nil {
		t.Fatalf("simulateDelivery: %v", err)
	}

	data, err := b.getDelivery(&adcp.GetMediaBuyDeliveryRequest{MediaBuyIDs: []string{buy.MediaBuyID}})
	if err != nil {
		t.Fatalf("getDelivery: %v", err)
	}
	packages := data.MediaBuyDeliveries[0].ByPackage
	if len(packages) != 2 {
		t.Fatalf("want 2 package rows, got %d", len(packages))
	}
	if packages[0].Spend != 10 || packages[1].Spend != 30 {
		t.Errorf("want spend weighted by 100/300 budget split, got %.2f/%.2f", packages[0].Spend, packages[1].Spend)
	}
}

func TestDeliveryReporting_ZeroSpendInitially(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, true)

	data, err := b.getDelivery(&adcp.GetMediaBuyDeliveryRequest{
		MediaBuyIDs: []string{buy.MediaBuyID},
	})
	if err != nil {
		t.Fatalf("getDelivery: %v", err)
	}
	totals := data.MediaBuyDeliveries[0].Totals
	if totals.Spend != 0 || totals.Impressions != 0 {
		t.Errorf("want zero delivery before simulation, got spend=%.2f impressions=%.0f", totals.Spend, totals.Impressions)
	}
}

// --- comply_test_controller custom scenarios ---

func TestCustomScenario_SeedProduct(t *testing.T) {
	b := newTestBackend()

	_, err := b.handleCustomScenario("seed_product", map[string]any{
		"product_id": "test-video-product",
		"fixture": map[string]any{
			"channels":      []any{"video"},
			"delivery_type": "non_guaranteed",
		},
	})
	if err != nil {
		t.Fatalf("seed_product: %v", err)
	}

	b.mu.RLock()
	p, ok := b.products["test-video-product"]
	b.mu.RUnlock()
	if !ok {
		t.Fatal("expected seeded product in products map")
	}
	if p.DeliveryType != "non_guaranteed" {
		t.Errorf("want delivery_type non_guaranteed, got %s", p.DeliveryType)
	}
}

func TestCustomScenario_SeedProduct_MissingID(t *testing.T) {
	b := newTestBackend()
	_, err := b.handleCustomScenario("seed_product", map[string]any{})
	if err == nil {
		t.Fatal("expected error when product_id is missing")
	}
}

func TestCustomScenario_SeedPricingOption(t *testing.T) {
	b := newTestBackend()

	_, err := b.handleCustomScenario("seed_pricing_option", map[string]any{
		"product_id":        "premium-display",
		"pricing_option_id": "custom-cpm-5",
		"fixture": map[string]any{
			"pricing_model": "cpm",
			"fixed_price":   5.0,
			"currency":      "USD",
		},
	})
	if err != nil {
		t.Fatalf("seed_pricing_option: %v", err)
	}

	b.mu.RLock()
	p := b.products["premium-display"]
	b.mu.RUnlock()
	found := false
	for _, opt := range p.PricingOptions {
		if opt.PricingOptionID == "custom-cpm-5" {
			found = true
			if opt.FixedPrice != 5.0 {
				t.Errorf("want fixed_price 5.0, got %.2f", opt.FixedPrice)
			}
		}
	}
	if !found {
		t.Error("seeded pricing option not found on product")
	}
}

func TestCustomScenario_ForceCreateMediaBuyArm_Submitted(t *testing.T) {
	b := newTestBackend()

	_, err := b.handleCustomScenario("force_create_media_buy_arm", map[string]any{
		"arm":     "submitted",
		"task_id": "task-abc-123",
		"message": "processing in async queue",
	})
	if err != nil {
		t.Fatalf("force_create_media_buy_arm: %v", err)
	}

	resp, err := b.createMediaBuyResponse(&adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: "premium-display", Budget: 500}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy after forced arm: %v", err)
	}
	submitted, ok := resp.(*adcp.CreateMediaBuySubmitted)
	if !ok {
		t.Fatalf("want submitted response after forced arm, got %T", resp)
	}
	if submitted.Status != "submitted" {
		t.Errorf("want submitted status after forced arm, got %s", submitted.Status)
	}
	if submitted.TaskID != "task-abc-123" {
		t.Errorf("want task_id task-abc-123, got %v", submitted.TaskID)
	}
	if submitted.Message != "processing in async queue" {
		t.Errorf("want async message, got %q", submitted.Message)
	}

	// Forced arm is consumed — next create should be normal.
	next, err := b.createMediaBuy(&adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: "premium-display", Budget: 500}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy after arm consumed: %v", err)
	}
	if next.Status == "submitted" {
		t.Error("forced arm should be consumed after first use")
	}
}

func TestCustomScenario_ForceCreateMediaBuyArm_UnknownArm(t *testing.T) {
	b := newTestBackend()
	_, err := b.handleCustomScenario("force_create_media_buy_arm", map[string]any{
		"arm": "rejected",
	})
	if err == nil {
		t.Fatal("expected error for unsupported arm value")
	}
}

func TestCustomScenario_Unknown(t *testing.T) {
	b := newTestBackend()
	_, err := b.handleCustomScenario("nonexistent_scenario", nil)
	if err == nil {
		t.Fatal("expected error for unknown scenario name")
	}
}

// --- ForceMediaBuyStatus ---

func TestForceMediaBuyStatus(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)

	tr, err := b.forceMediaBuyStatus(buy.MediaBuyID, "paused", "")
	if err != nil {
		t.Fatalf("forceMediaBuyStatus: %v", err)
	}
	if !tr.Success {
		t.Error("expected Success=true")
	}
	if tr.PreviousState != "pending_creatives" {
		t.Errorf("want previous state pending_creatives, got %s", tr.PreviousState)
	}
	if tr.CurrentState != "paused" {
		t.Errorf("want current state paused, got %s", tr.CurrentState)
	}
}

func TestForceMediaBuyStatus_TerminalStateBlocked(t *testing.T) {
	b := newTestBackend()
	buy := mustCreateBuy(t, b, false)
	canceled := true
	_, _, _ = b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: buy.MediaBuyID, Canceled: &canceled})

	_, err := b.forceMediaBuyStatus(buy.MediaBuyID, "active", "")
	if err == nil {
		t.Error("expected error when forcing status on canceled buy")
	}
}

func TestForceMediaBuyStatus_NotFound(t *testing.T) {
	b := newTestBackend()
	_, err := b.forceMediaBuyStatus("nonexistent-id", "active", "")
	if err == nil {
		t.Error("expected error for unknown media buy ID")
	}
}

func TestValidActions_UnknownStatusFailsClosed(t *testing.T) {
	if got := validActions("future_status"); len(got) != 0 {
		t.Fatalf("unknown media buy status should expose no valid actions, got %#v", got)
	}
}

func TestValidActions_CoversEveryKnownMediaBuyStatus(t *testing.T) {
	expected := map[adcp.MediaBuyStatus][]string{
		adcp.MediaBuyStatusPendingCreatives: {"cancel", "sync_creatives", "update_packages"},
		adcp.MediaBuyStatusPendingStart:     {"cancel", "sync_creatives", "update_packages"},
		adcp.MediaBuyStatusActive:           {"pause", "cancel", "sync_creatives", "update_packages"},
		adcp.MediaBuyStatusPaused:           {"resume", "cancel", "sync_creatives", "update_packages"},
		adcp.MediaBuyStatusCompleted:        {},
		adcp.MediaBuyStatusRejected:         {},
		adcp.MediaBuyStatusCanceled:         {},
	}

	for _, status := range adcp.KnownMediaBuyStatusValues() {
		want, ok := expected[status]
		if !ok {
			t.Fatalf("validActions missing explicit expectation for known status %q", status)
		}
		got := validActions(string(status))
		if !equalStringSlices(got, want) {
			t.Fatalf("validActions(%q) = %#v, want %#v", status, got, want)
		}
		for _, action := range want {
			if !hasValidAction(string(status), action) {
				t.Fatalf("hasValidAction(%q, %q) = false; valid actions %#v", status, action, got)
			}
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
