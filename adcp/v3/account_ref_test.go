package adcp

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// An account_id-only AccountReference must be handed to the resolver so an
// unknown ID yields ACCOUNT_NOT_FOUND instead of silently running with no
// account. See adcontextprotocol/adcp-go#547.
func TestResolveAccountIDOnlyGoesToResolver(t *testing.T) {
	var got AccountReference
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		got = ref
		return "acct-1", nil
	}

	acct, result := resolveAccount(context.Background(), resolver, AccountReference{AccountID: "acc_123"})
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if acct != "acct-1" {
		t.Fatalf("expected resolved account, got %v", acct)
	}
	if got.AccountID != "acc_123" {
		t.Fatalf("resolver saw wrong reference: %+v", got)
	}
}

func TestResolveAccountIDOnlyPointerGoesToResolver(t *testing.T) {
	called := false
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		called = true
		return "acct-1", nil
	}

	acct, result := resolveAccount(context.Background(), resolver, &AccountReference{AccountID: "acc_123"})
	if result != nil || acct != "acct-1" || !called {
		t.Fatalf("account_id-only *AccountReference must reach the resolver (acct=%v, result=%v, called=%v)", acct, result, called)
	}
}

func TestResolveAccountIDOnlyUnknownYieldsNotFound(t *testing.T) {
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		return nil, nil
	}

	_, result := resolveAccount(context.Background(), resolver, AccountReference{AccountID: "acc_nope"})
	if result == nil {
		t.Fatal("expected ACCOUNT_NOT_FOUND result for unknown account_id")
	}
	if !result.IsError {
		t.Fatal("expected IsError on unknown account_id result")
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
}

func TestResolveAccountEmptyRefIsStillNoAccount(t *testing.T) {
	// These are the decoded representations of a request whose account field
	// was omitted (or carried no identifier), not schema wire payloads.
	cases := map[string]any{
		"nil pointer":       (*AccountReference)(nil),
		"empty value":       AccountReference{},
		"sandbox flag only": AccountReference{Sandbox: true},
		// {"account_id": ""} is schema-valid but names nothing.
		"empty account_id": AccountReference{AccountID: ""},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			resolver := func(ctx context.Context, ref AccountReference) (any, error) {
				called = true
				return nil, nil
			}
			acct, result := resolveAccount(context.Background(), resolver, ref)
			if acct != nil || result != nil || called {
				t.Fatalf("empty account reference must stay the no-account path (acct=%v, result=%v, called=%v)", acct, result, called)
			}
		})
	}
}

func TestResolveAccountBrandRefStillResolves(t *testing.T) {
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		return "brand-acct", nil
	}
	// The schema's natural-key example: brand.domain + operator (account-ref.json).
	ref := AccountReference{Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"}
	acct, result := resolveAccount(context.Background(), resolver, ref)
	if result != nil || acct != "brand-acct" {
		t.Fatalf("brand-only reference must keep resolving (acct=%v, result=%v)", acct, result)
	}
}

func TestResolveAccountResolverErrorSurfaces(t *testing.T) {
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		return nil, errors.New("boom")
	}
	_, result := resolveAccount(context.Background(), resolver, AccountReference{AccountID: "acc_123"})
	if result == nil || !result.IsError {
		t.Fatal("resolver errors must surface as an error result")
	}
}

func TestResolveAccountNilResolverIsNoAccount(t *testing.T) {
	acct, result := resolveAccount(context.Background(), nil, AccountReference{AccountID: "acc_123"})
	if acct != nil || result != nil {
		t.Fatalf("nil resolver must keep the no-account path (acct=%v, result=%v)", acct, result)
	}
}

// The ACCOUNT_NOT_FOUND guidance must match the reference form: an unknown
// account_id points at list_accounts (and the error is terminal, per
// enums/error-code.json), while a natural key points at sync_accounts.
func TestResolveAccountNotFoundGuidanceBranchesOnForm(t *testing.T) {
	resolver := func(ctx context.Context, ref AccountReference) (any, error) {
		return nil, nil
	}
	payloadOf := func(ref AccountReference) map[string]any {
		_, result := resolveAccount(context.Background(), resolver, ref)
		if result == nil {
			t.Fatal("expected ACCOUNT_NOT_FOUND result")
		}
		wire, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("expected map wire shape, got %T", result.StructuredContent)
		}
		payload, ok := wire["adcp_error"].(map[string]any)
		if !ok {
			t.Fatalf("expected adcp_error payload, got %v", wire)
		}
		return payload
	}

	byID := payloadOf(AccountReference{AccountID: "acc_nope"})
	if byID["recovery"] != "terminal" {
		t.Fatalf("account_id ACCOUNT_NOT_FOUND must be terminal, got %v", byID["recovery"])
	}
	if s, _ := byID["suggestion"].(string); !strings.Contains(s, "list_accounts") {
		t.Fatalf("account_id suggestion must point at list_accounts, got %q", s)
	}

	byKey := payloadOf(AccountReference{Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"})
	if s, _ := byKey["suggestion"].(string); !strings.Contains(s, "sync_accounts") {
		t.Fatalf("natural-key suggestion must point at sync_accounts, got %q", s)
	}
}

// A supplied reference that matches neither form in the pinned
// core/account-ref.json schema must be rejected with INVALID_REQUEST
// before the resolver runs — in particular an operator-only reference must
// never reach a resolver with a nil Brand. See #547 / the #554 hold.
func TestResolveAccountInvalidFormsRejected(t *testing.T) {
	cases := map[string]AccountReference{
		"operator only":         {Operator: "acme-corp.com"},
		"brand only":            {Brand: &BrandReference{Domain: "acme-corp.com"}},
		"brand without domain":  {Brand: &BrandReference{BrandID: "spark"}, Operator: "acme-corp.com"},
		"account_id + operator": {AccountID: "acc_123", Operator: "acme-corp.com"},
		"account_id + brand":    {AccountID: "acc_123", Brand: &BrandReference{Domain: "acme-corp.com"}},
		"account_id + key":      {AccountID: "acc_123", Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com"},
		"operator not a domain": {Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "Acme_Corp!"},
		"brand domain invalid":  {Brand: &BrandReference{Domain: "-acme-.com"}, Operator: "acme-corp.com"},
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
			if payload["recovery"] != "correctable" {
				t.Fatalf("INVALID_REQUEST recovery must be correctable, got %v", payload["recovery"])
			}
		})
	}
}

// Sandbox is a modifier on the flattened Go type, not an identifier: it
// does not change which schema form a reference is, and a sandbox-bearing
// reference of either valid form still reaches the resolver (which is
// responsible for sandbox semantics).
func TestResolveAccountSandboxFormsStillResolve(t *testing.T) {
	cases := map[string]AccountReference{
		"account_id + sandbox": {AccountID: "acc_123", Sandbox: true},
		"natural key + sandbox": {
			Brand: &BrandReference{Domain: "acme-corp.com"}, Operator: "acme-corp.com", Sandbox: true,
		},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			var got AccountReference
			resolver := func(ctx context.Context, ref AccountReference) (any, error) {
				got = ref
				return "acct-1", nil
			}
			acct, result := resolveAccount(context.Background(), resolver, ref)
			if result != nil || acct != "acct-1" || !got.Sandbox {
				t.Fatalf("sandbox-bearing valid form must reach the resolver (acct=%v, result=%v, got=%+v)", acct, result, got)
			}
		})
	}
}

// TestRegisteredAccountLifecycle exercises the combined bug 1 + bug 3
// behavior of #547 against the updated SDK, through the registered tools,
// with a resolver written the way the reference sellers now are: lookup
// only, provisioning exclusively via sync_accounts.
//
//   - an unknown account_id and an unknown natural key both answer
//     ACCOUNT_NOT_FOUND, and the discovery call creates nothing;
//   - a schema-invalid (operator-only) reference answers INVALID_REQUEST;
//   - after sync_accounts provisions the account, get_products succeeds
//     by natural key and by the returned account_id.
func TestRegisteredAccountLifecycle(t *testing.T) {
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

	syncResult := callRegisteredTool(t, cfg, "sync_accounts", map[string]any{
		"idempotency_key": "lifecycle-sync-1",
		"accounts":        []any{naturalKey},
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
