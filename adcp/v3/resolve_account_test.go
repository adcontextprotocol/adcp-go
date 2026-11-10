package adcp

import (
	"context"
	"testing"
)

func TestResolveAccountAccountIDOnlyReachesResolver(t *testing.T) {
	called := false
	resolver := func(_ context.Context, ref AccountReference) (any, error) {
		called = true
		if ref.AccountID != "acc_123" {
			t.Errorf("resolver got account_id %q", ref.AccountID)
		}
		return nil, nil
	}
	acct, res := resolveAccount(context.Background(), resolver, &AccountReference{AccountID: "acc_123"})
	if !called {
		t.Fatal("account_id-only reference never reached the resolver")
	}
	if acct != nil || res == nil || !res.IsError {
		t.Fatalf("want ACCOUNT_NOT_FOUND error result, got acct=%v res=%v", acct, res)
	}
}
