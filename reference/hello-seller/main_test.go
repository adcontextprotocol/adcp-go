package main

import (
	"context"
	"testing"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
)

// TestHelloSellerSmoke exercises the full core-tier tool invocation path
// directly against the handler functions (no HTTP, no MCP transport) — the
// smoke test the issue's acceptance criteria asks for: get_products, then
// create_media_buy, then get_media_buys, confirming the created buy is
// retrievable.
func TestHelloSellerSmoke(t *testing.T) {
	b := newBackend()
	ctx := context.Background()

	products, err := b.getProducts(ctx, nil, &adcp.GetProductsRequest{})
	if err != nil {
		t.Fatalf("getProducts: %v", err)
	}
	if len(products.Products) != 1 {
		t.Fatalf("want 1 product, got %d", len(products.Products))
	}
	product := products.Products[0]

	resp, err := b.createMediaBuy(ctx, nil, &adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: product.ProductID, PricingOptionID: "pd-cpm-15", Budget: 1000}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}
	success, ok := resp.(*adcp.CreateMediaBuySuccess)
	if !ok {
		t.Fatalf("want *adcp.CreateMediaBuySuccess, got %T", resp)
	}
	if success.MediaBuyID == "" {
		t.Fatal("expected non-empty MediaBuyID")
	}
	if success.MediaBuyStatus != "active" {
		t.Errorf("want status active, got %q", success.MediaBuyStatus)
	}

	got, err := b.getMediaBuys(ctx, nil, &adcp.GetMediaBuysRequest{MediaBuyIDs: []string{success.MediaBuyID}})
	if err != nil {
		t.Fatalf("getMediaBuys: %v", err)
	}
	if len(got.MediaBuys) != 1 {
		t.Fatalf("want 1 media buy, got %d", len(got.MediaBuys))
	}
	if got.MediaBuys[0].MediaBuyID != success.MediaBuyID {
		t.Errorf("want media buy %q, got %q", success.MediaBuyID, got.MediaBuys[0].MediaBuyID)
	}
}

// TestUpdateMediaBuy_PauseAndCancel exercises the extension-tier
// update_media_buy handler: pause, then resume, then cancel, confirming
// each transition is reflected and the revision counter advances.
func TestUpdateMediaBuy_PauseAndCancel(t *testing.T) {
	b := newBackend()
	resp, err := b.createMediaBuy(context.Background(), nil, &adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: "premium-display", PricingOptionID: "pd-cpm-15", Budget: 1000}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}
	id := resp.(*adcp.CreateMediaBuySuccess).MediaBuyID

	_, out, err := b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: id, Paused: adcp.Ptr(true)})
	if err != nil {
		t.Fatalf("updateMediaBuy(pause): %v", err)
	}
	if wire, ok := out.(map[string]any); !ok || wire["status"] != "paused" {
		t.Fatalf("want status paused, got %#v", out)
	}

	_, out, err = b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: id, Canceled: adcp.Ptr(true)})
	if err != nil {
		t.Fatalf("updateMediaBuy(cancel): %v", err)
	}
	wire, ok := out.(map[string]any)
	if !ok || wire["status"] != "canceled" {
		t.Fatalf("want status canceled, got %#v", out)
	}
	// out is the SDK's raw pre-round-trip output (unlike result.StructuredContent,
	// which is JSON-round-tripped and would turn this into a float64).
	if rev, ok := wire["revision"].(int); !ok || rev != 3 {
		t.Errorf("want revision 3 after create+pause+cancel, got %#v", wire["revision"])
	}

	// A canceled buy cannot be canceled again. Tool-level errors surface via
	// result.IsError (err stays nil) — the same MCP convention
	// adcp/v3/responses_test.go's TestMediaBuyResponseSubmittedRequiresTaskID
	// exercises for create_media_buy.
	result, _, err := b.updateMediaBuy(adcp.UpdateMediaBuyRequest{MediaBuyID: id, Canceled: adcp.Ptr(true)})
	if err != nil {
		t.Fatalf("updateMediaBuy(re-cancel): %v", err)
	}
	if !result.IsError {
		t.Error("want IsError=true re-canceling an already-canceled media buy")
	}
}

// TestSyncAndListCreatives exercises the extension-tier sync_creatives and
// list_creatives handlers together: synced creatives must be retrievable.
func TestSyncAndListCreatives(t *testing.T) {
	b := newBackend()
	_, err := b.syncCreatives(context.Background(), &adcp.SyncCreativesRequest{
		Creatives: []adcp.CreativeInput{{CreativeID: "cr-1", Name: "Test Creative"}},
	})
	if err != nil {
		t.Fatalf("syncCreatives: %v", err)
	}

	result, _, err := b.listCreatives(adcp.ListCreativesRequest{})
	if err != nil {
		t.Fatalf("listCreatives: %v", err)
	}
	wire, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("want map[string]any StructuredContent, got %T", result.StructuredContent)
	}
	creatives, ok := wire["creatives"].([]any)
	if !ok || len(creatives) != 1 {
		t.Fatalf("want 1 creative, got %#v", wire["creatives"])
	}
	creative, ok := creatives[0].(map[string]any)
	if !ok || creative["creative_id"] != "cr-1" {
		t.Errorf("want creative_id cr-1, got %#v", creatives[0])
	}
}

// TestGetDelivery exercises the extension-tier get_media_buy_delivery
// handler against a created buy.
func TestGetDelivery(t *testing.T) {
	b := newBackend()
	resp, err := b.createMediaBuy(context.Background(), nil, &adcp.CreateMediaBuyRequest{
		Packages: []adcp.PackageInput{{ProductID: "premium-display", PricingOptionID: "pd-cpm-15", Budget: 1000}},
	})
	if err != nil {
		t.Fatalf("createMediaBuy: %v", err)
	}
	id := resp.(*adcp.CreateMediaBuySuccess).MediaBuyID

	data, err := b.getDelivery(context.Background(), nil, &adcp.GetMediaBuyDeliveryRequest{MediaBuyIDs: []string{id}})
	if err != nil {
		t.Fatalf("getDelivery: %v", err)
	}
	if len(data.MediaBuyDeliveries) != 1 {
		t.Fatalf("want 1 delivery row, got %d", len(data.MediaBuyDeliveries))
	}
	if data.MediaBuyDeliveries[0].MediaBuyID != id {
		t.Errorf("want media buy %q, got %q", id, data.MediaBuyDeliveries[0].MediaBuyID)
	}
}

// TestNewServer_RegistersWithoutPanicking proves adcp.Register's mandatory
// invariants (IdempotencyReplayTTL non-zero, etc.) are actually satisfied —
// Register panics on misconfiguration, so a clean construction is itself the
// test.
func TestNewServer_RegistersWithoutPanicking(t *testing.T) {
	server := newServer(newBackend())
	if server == nil {
		t.Fatal("expected non-nil server")
	}
}

// TestResolveAccountUnknownDoesNotProvision pins bug 3 of adcp-go#547: a
// lookup for an unknown account — by natural key or by account_id — must
// return no account and must not create one. Provisioning happens only
// through sync_accounts.
func TestResolveAccountUnknownDoesNotProvision(t *testing.T) {
	b := newBackend()
	ctx := context.Background()

	refs := map[string]adcp.AccountReference{
		"natural key": {Brand: &adcp.BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"},
		"account_id":  {AccountID: "acc_nope"},
	}
	for name, ref := range refs {
		acct, err := b.resolveAccount(ctx, ref)
		if err != nil {
			t.Fatalf("%s: resolveAccount: %v", name, err)
		}
		if acct != nil {
			t.Fatalf("%s: unknown account must resolve to nil, got %v", name, acct)
		}
	}
	if len(b.accounts) != 0 {
		t.Fatalf("resolution must not create accounts, got %d", len(b.accounts))
	}
}

// TestSyncAccountsProvisionsThenResolves exercises the combined behavior:
// after sync_accounts provisions an account, resolveAccount finds it by
// natural key and by the returned account_id; a re-sync reports "updated".
func TestSyncAccountsProvisionsThenResolves(t *testing.T) {
	b := newBackend()
	ctx := context.Background()

	results, err := b.syncAccounts(ctx, &adcp.SyncAccountsRequest{
		Accounts: []adcp.AccountInput{{Brand: &adcp.BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"}},
	})
	if err != nil {
		t.Fatalf("syncAccounts: %v", err)
	}
	if len(results) != 1 || results[0].Action != "created" || results[0].AccountID == "" {
		t.Fatalf("want one created account with an id, got %+v", results)
	}
	id := results[0].AccountID

	byKey, err := b.resolveAccount(ctx, adcp.AccountReference{Brand: &adcp.BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"})
	if err != nil || byKey == nil {
		t.Fatalf("provisioned account must resolve by natural key (acct=%v, err=%v)", byKey, err)
	}
	byID, err := b.resolveAccount(ctx, adcp.AccountReference{AccountID: id})
	if err != nil || byID == nil {
		t.Fatalf("provisioned account must resolve by account_id (acct=%v, err=%v)", byID, err)
	}

	results, err = b.syncAccounts(ctx, &adcp.SyncAccountsRequest{
		Accounts: []adcp.AccountInput{{Brand: &adcp.BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"}},
	})
	if err != nil {
		t.Fatalf("syncAccounts (re-sync): %v", err)
	}
	if len(results) != 1 || results[0].Action != "updated" {
		t.Fatalf("re-sync must report updated, got %+v", results)
	}
}
