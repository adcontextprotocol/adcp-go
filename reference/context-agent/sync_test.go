package contextagent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.NotNil(t, registry.Get(100), "expected RID 100 in registry")
	require.NotNil(t, registry.Get(200), "expected RID 200 in registry")
	require.True(t, targeting.ContainsProperty(100), "expected RID 100 in targeting bitmap")
	require.True(t, targeting.ContainsProperty(200), "expected RID 200 in targeting bitmap")
	assert.Equal(t, uint64(2), registry.Sequence)
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

	assert.Nil(t, registry.Get(100), "expected RID 100 removed from registry")
	assert.False(t, targeting.ContainsProperty(100), "expected RID 100 removed from targeting bitmap")
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
	require.NotNil(t, rec, "expected RID 100 in registry after update")
	assert.Equal(t, "new.com", rec.Domain)
	assert.True(t, targeting.ContainsProperty(100), "expected RID 100 still in targeting after update")
}

func TestApplyEvents_EmptyEvents(t *testing.T) {
	registry := NewPropertyRegistry()
	targeting := NewTargetingConfig()

	ApplyEvents(registry, targeting, nil)

	assert.Equal(t, uint64(0), registry.Sequence)
}
