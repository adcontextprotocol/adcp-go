package adcp

import (
	"context"
	"strings"
	"testing"
)

// The v1 resolveAccount guard mirrors v3: an account_id-only reference names
// an account and must reach the resolver instead of silently running with no
// account. See adcontextprotocol/adcp-go#547 and the review of #554.
func TestResolveAccountIDOnlyGoesToResolverV1(t *testing.T) {
	var got AccountReference
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		got = ref
		return "acct-1", nil
	}

	acct, result := resolveAccount(context.Background(), resolver, AccountReference{AccountID: "acc_123"})
	if result != nil || acct != "acct-1" || got.AccountID != "acc_123" {
		t.Fatalf("account_id-only reference must reach the resolver (acct=%v, result=%v, got=%+v)", acct, result, got)
	}
}

func TestResolveAccountIDOnlyUnknownYieldsNotFoundV1(t *testing.T) {
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		return nil, nil
	}

	_, result := resolveAccount(context.Background(), resolver, AccountReference{AccountID: "acc_nope"})
	if result == nil || !result.IsError {
		t.Fatal("expected ACCOUNT_NOT_FOUND error result for unknown account_id")
	}
	wire, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map wire shape, got %T", result.StructuredContent)
	}
	payload, ok := wire["adcp_error"].(map[string]any)
	if !ok {
		t.Fatalf("expected adcp_error payload, got %v", wire)
	}
	if payload["code"] != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("expected ACCOUNT_NOT_FOUND, got %v", payload["code"])
	}
	if s, _ := payload["suggestion"].(string); !strings.Contains(s, "list_accounts") {
		t.Fatalf("account_id suggestion must point at list_accounts, got %q", s)
	}
}

// The v1 package enforces the same pinned-schema validation as v3: a
// supplied reference matching neither account-ref form is rejected with
// INVALID_REQUEST before the resolver runs.
func TestResolveAccountInvalidFormsRejectedV1(t *testing.T) {
	cases := map[string]AccountReference{
		"operator only":         {Operator: "acme-corp.com"},
		"brand only":            {Brand: &BrandReference{Domain: "acme-corp.com"}},
		"account_id + operator": {AccountID: "acc_123", Operator: "acme-corp.com"},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			resolver := func(ctx context.Context, ref AccountReference) (any, error) {
				called = true
				return "acct-1", nil
			}
			acct, result := resolveAccount(context.Background(), resolver, ref)
			if called {
				t.Fatal("resolver must not be invoked for a schema-invalid reference")
			}
			if acct != nil || result == nil || !result.IsError {
				t.Fatalf("expected INVALID_REQUEST error result (acct=%v, result=%v)", acct, result)
			}
			wire, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("expected map wire shape, got %T", result.StructuredContent)
			}
			payload, ok := wire["adcp_error"].(map[string]any)
			if !ok {
				t.Fatalf("expected adcp_error payload, got %v", wire)
			}
			if payload["code"] != "INVALID_REQUEST" {
				t.Fatalf("expected INVALID_REQUEST, got %v", payload["code"])
			}
		})
	}
}

// TestRegisteredAccountLifecycleV1 mirrors the v3 lifecycle test: the
// combined bug 1 + bug 3 behavior of #547 against the updated v1 SDK,
// through the registered tools, with a lookup-only resolver whose only
// provisioning path is sync_accounts.
func TestRegisteredAccountLifecycleV1(t *testing.T) {
	accounts := map[string]*AccountResult{}
	deriveID := func(ref AccountReference) string {
		if ref.AccountID != "" {
			return ref.AccountID
		}
		domain := ""
		if ref.Brand != nil {
			domain = ref.Brand.Domain
		}
		return "acct-" + domain + "-" + ref.Operator
	}
	cfg := baseTestConfig(Config{
		ResolveAccount: func(_ context.Context, ref AccountReference) (any, error) {
			if acct, ok := accounts[deriveID(ref)]; ok {
				return acct, nil
			}
			return nil, nil
		},
		SyncAccounts: func(_ context.Context, req *SyncAccountsRequest) ([]AccountResult, error) {
			results := make([]AccountResult, 0, len(req.Accounts))
			for _, acct := range req.Accounts {
				id := deriveID(AccountReference{Brand: acct.Brand, Operator: acct.Operator})
				result := AccountResult{AccountID: id, Brand: acct.Brand, Operator: acct.Operator, Action: "created", Status: "active"}
				if _, ok := accounts[id]; ok {
					result.Action = "updated"
				}
				accounts[id] = &result
				results = append(results, result)
			}
			return results, nil
		},
		GetProducts: func(_ context.Context, _ any, _ *GetProductsRequest) (*ProductsData, error) {
			return &ProductsData{Products: []Product{}}, nil
		},
	})

	codeOf := func(args map[string]any) string {
		// buying_mode is required by get-products-request.json.
		args["buying_mode"] = "brief"
		result := callRegisteredTool(t, cfg, "get_products", args)
		if !result.IsError {
			return ""
		}
		payload, ok := structuredContentMap(t, result)["adcp_error"].(map[string]any)
		if !ok {
			t.Fatalf("expected adcp_error payload for %v", args)
		}
		code, _ := payload["code"].(string)
		return code
	}

	naturalKey := map[string]any{"brand": map[string]any{"domain": "acme-corp.com"}, "operator": "acme-corp.com"}

	if code := codeOf(map[string]any{"account": map[string]any{"account_id": "acc_nope"}}); code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown account_id: want ACCOUNT_NOT_FOUND, got %q", code)
	}
	if code := codeOf(map[string]any{"account": naturalKey}); code != "ACCOUNT_NOT_FOUND" {
		t.Fatalf("unknown natural key: want ACCOUNT_NOT_FOUND, got %q", code)
	}
	if len(accounts) != 0 {
		t.Fatalf("discovery calls must not provision accounts, got %d", len(accounts))
	}
	if code := codeOf(map[string]any{"account": map[string]any{"operator": "acme-corp.com"}}); code != "INVALID_REQUEST" {
		t.Fatalf("operator-only reference: want INVALID_REQUEST, got %q", code)
	}

	// Provisioning-mode entries carry billing (sync-accounts-request.json).
	syncEntry := map[string]any{"brand": map[string]any{"domain": "acme-corp.com"}, "operator": "acme-corp.com", "billing": "operator"}
	syncResult := callRegisteredTool(t, cfg, "sync_accounts", map[string]any{
		"idempotency_key": "lifecycle-sync-1",
		"accounts":        []any{syncEntry},
	})
	if syncResult.IsError {
		t.Fatalf("sync_accounts must provision, got %v", structuredContentMap(t, syncResult))
	}
	if code := codeOf(map[string]any{"account": naturalKey}); code != "" {
		t.Fatalf("provisioned natural key must resolve, got error %q", code)
	}
	if code := codeOf(map[string]any{"account": map[string]any{"account_id": "acct-acme-corp.com-acme-corp.com"}}); code != "" {
		t.Fatalf("provisioned account_id must resolve, got error %q", code)
	}
}

// Schema edges from the pinned bundle (review of #554): the account_id arm
// of core/account-ref.json is additionalProperties:false, so sandbox may
// not accompany account_id — while the natural-key arm carries sandbox as
// a modifier (the schema's own fifth example). A present brand_id must
// match core/brand-id.json (^[a-z0-9_]+$).
func TestResolveAccountSchemaEdgeFormsV1(t *testing.T) {
	invalid := map[string]AccountReference{
		"account_id + sandbox":   {AccountID: "acc_123", Sandbox: true},
		"brand_id with capitals": {Brand: &BrandReference{Domain: "nova-brands.com", BrandID: "Spark"}, Operator: "pinnacle-media.com"},
		"brand_id with dash":     {Brand: &BrandReference{Domain: "nova-brands.com", BrandID: "spark-1"}, Operator: "pinnacle-media.com"},
	}
	for name, ref := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			called := false
			resolver := func(ctx context.Context, ref AccountReference) (any, error) {
				called = true
				return "acct-1", nil
			}
			_, result := resolveAccount(context.Background(), resolver, ref)
			if called {
				t.Fatal("resolver must not be invoked for a schema-invalid reference")
			}
			if result == nil || !result.IsError {
				t.Fatalf("expected INVALID_REQUEST error result, got %v", result)
			}
			wire, _ := result.StructuredContent.(map[string]any)
			payload, _ := wire["adcp_error"].(map[string]any)
			if payload["code"] != "INVALID_REQUEST" {
				t.Fatalf("expected INVALID_REQUEST, got %v", payload["code"])
			}
		})
	}
	valid := map[string]AccountReference{
		"brand_id valid":        {Brand: &BrandReference{Domain: "nova-brands.com", BrandID: "spark"}, Operator: "pinnacle-media.com"},
		"natural key + sandbox": {Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com", Sandbox: true},
	}
	for name, ref := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			called := false
			resolver := func(ctx context.Context, ref AccountReference) (any, error) {
				called = true
				return "acct-1", nil
			}
			acct, result := resolveAccount(context.Background(), resolver, ref)
			if !called || result != nil || acct != "acct-1" {
				t.Fatalf("schema-valid reference must reach the resolver (called=%v, acct=%v, result=%v)", called, acct, result)
			}
		})
	}
}
