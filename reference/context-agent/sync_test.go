package contextagent

import (
	"testing"
)

func TestApplyEvents_RegisterAddsToTargeting(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	events := []RegistryEvent{
		{
			Sequence: 1,
			Action:   "register",
			Record:   PropertyRecord{RID: 100, Domain: "example.com"},
		},
		{
			Sequence: 2,
			Action:   "register",
			Record:   PropertyRecord{RID: 200, Domain: "test.com"},
		},
	}

	ApplyEvents(registry, targeting, events)

	if registry.Get(100) == nil {
		t.Fatal("expected RID 100 in registry")
	}
	if registry.Get(200) == nil {
		t.Fatal("expected RID 200 in registry")
	}
	if !targeting.ContainsProperty(100) {
		t.Fatal("expected RID 100 in targeting bitmap")
	}
	if !targeting.ContainsProperty(200) {
		t.Fatal("expected RID 200 in targeting bitmap")
	}
	if registry.Sequence != 2 {
		t.Fatalf("expected sequence 2, got %d", registry.Sequence)
	}
}

func TestApplyEvents_DeactivateRemovesFromTargeting(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	ApplyEvents(registry, targeting, []RegistryEvent{
		{Sequence: 1, Action: "register", Record: PropertyRecord{RID: 100, Domain: "example.com"}},
	})

	ApplyEvents(registry, targeting, []RegistryEvent{
		{Sequence: 2, Action: "deactivate", Record: PropertyRecord{RID: 100}},
	})

	if registry.Get(100) != nil {
		t.Fatal("expected RID 100 removed from registry")
	}
	if targeting.ContainsProperty(100) {
		t.Fatal("expected RID 100 removed from targeting bitmap")
	}
}

func TestApplyEvents_UpdateExistingProperty(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	ApplyEvents(registry, targeting, []RegistryEvent{
		{Sequence: 1, Action: "register", Record: PropertyRecord{RID: 100, Domain: "old.com"}},
	})

	ApplyEvents(registry, targeting, []RegistryEvent{
		{Sequence: 2, Action: "update", Record: PropertyRecord{RID: 100, Domain: "new.com"}},
	})

	rec := registry.Get(100)
	if rec == nil {
		t.Fatal("expected RID 100 in registry after update")
	}
	if rec.Domain != "new.com" {
		t.Fatalf("expected updated domain, got %s", rec.Domain)
	}
	if !targeting.ContainsProperty(100) {
		t.Fatal("expected RID 100 still in targeting after update")
	}
}

func TestApplyEvents_EmptyEvents(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	ApplyEvents(registry, targeting, nil)

	if registry.Sequence != 0 {
		t.Fatalf("expected sequence 0 for empty events, got %d", registry.Sequence)
	}
}
