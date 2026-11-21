package main

import (
	"encoding/json"
	"testing"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
	"github.com/adcontextprotocol/adcp-go/router"
)

func TestRouterConfigFileIncludesTrustedContextNamespaceButAdminJSONRedactsIt(t *testing.T) {
	data, err := json.Marshal(routerConfigFile(routerConfig()))
	if err != nil {
		t.Fatalf("marshal generated router config: %v", err)
	}

	var loaded router.ServerConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("load generated router config: %v", err)
	}
	if len(loaded.Providers) != 2 {
		t.Fatalf("loaded %d providers, want 2", len(loaded.Providers))
	}
	if got := loaded.Providers[0].CacheNamespace; got != fixture.ContextCacheNamespace {
		t.Fatalf("context cache namespace = %q, want %q", got, fixture.ContextCacheNamespace)
	}
	if got := loaded.Providers[1].CacheNamespace; got != "" {
		t.Fatalf("identity cache namespace = %q, want empty", got)
	}

	adminJSON, err := json.Marshal(loaded.Providers[0])
	if err != nil {
		t.Fatalf("marshal admin provider JSON: %v", err)
	}
	var admin map[string]any
	if err := json.Unmarshal(adminJSON, &admin); err != nil {
		t.Fatalf("parse admin provider JSON: %v", err)
	}
	if _, exposed := admin["cache_namespace"]; exposed {
		t.Fatal("admin provider JSON exposed cache_namespace")
	}
}
