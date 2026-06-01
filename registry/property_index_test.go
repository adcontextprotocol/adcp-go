package registry

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPropertyIndex_PutAndLookup(t *testing.T) {
	idx := NewPropertyIndex()
	ctx := context.Background()

	p := &Property{
		PropertyID:   "pub1.example.com/homepage",
		PropertyRID:  1001,
		PropertyType: "website",
		Domain:       "example.com",
		Placements:   []string{"top-banner", "sidebar"},
	}
	require.NoError(t, idx.Put(ctx, p))

	require.Equal(t, 1, idx.Count())

	got, ok := idx.LookupByID("pub1.example.com/homepage")
	require.True(t, ok, "LookupByID: not found")
	assert.Equal(t, uint64(1001), got.PropertyRID)

	got, ok = idx.LookupByRID(1001)
	require.True(t, ok, "LookupByRID: not found")
	assert.Equal(t, "pub1.example.com/homepage", got.PropertyID)
	assert.Equal(t, "example.com", got.Domain)

	id, ok := idx.LookupByDomain("example.com")
	require.True(t, ok, "LookupByDomain: not found")
	assert.Equal(t, "pub1.example.com/homepage", id)

	assert.Equal(t, uint64(1001), idx.PropertyRID("pub1.example.com/homepage"))
	assert.Equal(t, uint64(0), idx.PropertyRID("nonexistent"))
}

func TestPropertyIndex_Update(t *testing.T) {
	idx := NewPropertyIndex()
	ctx := context.Background()

	require.NoError(t, idx.Put(ctx, &Property{PropertyID: "prop1", PropertyRID: 100, Domain: "old.com", PropertyType: "website"}))
	require.NoError(t, idx.Put(ctx, &Property{PropertyID: "prop1", PropertyRID: 200, Domain: "new.com", PropertyType: "ctv_app"}))

	require.Equal(t, 1, idx.Count())

	_, ok := idx.LookupByRID(100)
	assert.False(t, ok, "old RID 100 should be removed")

	got, ok := idx.LookupByRID(200)
	require.True(t, ok, "new RID 200 not found")
	assert.Equal(t, "ctv_app", got.PropertyType)

	_, ok = idx.LookupByDomain("old.com")
	assert.False(t, ok, "old domain should be removed")

	id, ok := idx.LookupByDomain("new.com")
	assert.True(t, ok)
	assert.Equal(t, "prop1", id)
}

func TestPropertyIndex_Remove(t *testing.T) {
	idx := NewPropertyIndex()
	ctx := context.Background()

	require.NoError(t, idx.Put(ctx, &Property{PropertyID: "prop1", PropertyRID: 100, Domain: "example.com"}))

	_, existed := idx.LookupByID("prop1")
	assert.True(t, existed, "should exist before remove")
	require.NoError(t, idx.Remove(ctx, "prop1"))
	require.NoError(t, idx.Remove(ctx, "prop1")) // idempotent
	assert.Equal(t, 0, idx.Count())

	_, ok := idx.LookupByRID(100)
	assert.False(t, ok, "RID should be removed")
	_, ok = idx.LookupByDomain("example.com")
	assert.False(t, ok, "domain should be removed")
}

func TestPropertyIndex_NoDomain(t *testing.T) {
	idx := NewPropertyIndex()
	ctx := context.Background()

	require.NoError(t, idx.Put(ctx, &Property{PropertyID: "prop1", PropertyRID: 100}))

	_, ok := idx.LookupByID("prop1")
	assert.True(t, ok, "should find by ID")
	_, ok = idx.LookupByRID(100)
	assert.True(t, ok, "should find by RID")
	require.NoError(t, idx.Remove(ctx, "prop1"))
	assert.Equal(t, 0, idx.Count(), "should be empty after remove")
}

func TestPropertyIndex_Concurrent(t *testing.T) {
	idx := NewPropertyIndex()
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := range 100 {
		wg.Add(1)
		go func(n uint64) {
			defer wg.Done()
			p := &Property{
				PropertyID:  "prop",
				PropertyRID: n,
				Domain:      "example.com",
			}
			_ = idx.Put(ctx, p)
		}(uint64(i)) //nolint:gosec // test values are small
	}

	for range 100 {
		wg.Go(func() {
			idx.LookupByID("prop")
			idx.LookupByRID(50)
			idx.LookupByDomain("example.com")
			idx.PropertyRID("prop")
			idx.Count()
		})
	}

	wg.Wait()

	assert.Equal(t, 1, idx.Count())
}
