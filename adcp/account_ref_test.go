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
