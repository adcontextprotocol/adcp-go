package adcp

import (
	"context"
	"errors"
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
	cases := map[string]any{
		"nil pointer":       (*AccountReference)(nil),
		"empty value":       AccountReference{},
		"sandbox flag only": AccountReference{Sandbox: true},
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
	ref := AccountReference{Brand: &BrandReference{Domain: "brand.example"}}
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
