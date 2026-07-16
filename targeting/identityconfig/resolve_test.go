package identityconfig

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRequest_EmptyPackageIDsReturnsSellerSet(t *testing.T) {
	src := newMemorySource()
	r1 := &targeting.SegmentRule{AnyOf: []string{"a"}}
	r2 := &targeting.SegmentRule{AnyOf: []string{"b"}}
	src.put("https://seller.example/agent", "pkg-1", r1, time.Unix(1, 0))
	src.put("https://seller.example/agent", "pkg-2", r2, time.Unix(1, 0))
	src.put("https://other.example/agent", "pkg-3", nil, time.Unix(1, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	effective, configs := ResolveRequest(svc, "https://seller.example/agent", nil)

	assert.ElementsMatch(t, []string{"pkg-1", "pkg-2"}, effective)
	require.Len(t, configs, 2)
	assert.Equal(t, r1, configs["pkg-1"].TargetSegments)
	assert.Equal(t, r2, configs["pkg-2"].TargetSegments)
}

func TestResolveRequest_FiltersIntersection(t *testing.T) {
	src := newMemorySource()
	src.put("https://seller.example/agent", "pkg-1", &targeting.SegmentRule{AnyOf: []string{"a"}}, time.Unix(1, 0))
	src.put("https://seller.example/agent", "pkg-2", &targeting.SegmentRule{AnyOf: []string{"b"}}, time.Unix(1, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// pkg-unknown is silently dropped (not registered for this seller).
	effective, configs := ResolveRequest(svc, "https://seller.example/agent", []string{"pkg-1", "pkg-unknown"})

	assert.Equal(t, []string{"pkg-1"}, effective)
	require.Len(t, configs, 1)
	require.Contains(t, configs, "pkg-1")
}

func TestResolveRequest_UnknownSellerReturnsEmpty(t *testing.T) {
	svc, err := New(newMemorySource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	effective, configs := ResolveRequest(svc, "https://unknown/agent", []string{"pkg-1"})
	assert.Empty(t, effective)
	assert.Empty(t, configs)
}

func TestResolveRequest_RegisteredSellerAllUnregisteredIDs(t *testing.T) {
	// Seller is registered with at least one package, but none of the
	// requested package IDs are in the registered set. Result must be
	// empty — the unregistered IDs are silently dropped (per the
	// registry-membership-leak invariant).
	src := newMemorySource()
	src.put("https://seller.example/agent", "pkg-registered", &targeting.SegmentRule{AnyOf: []string{"x"}}, time.Unix(1, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	effective, configs := ResolveRequest(svc, "https://seller.example/agent", []string{"pkg-unknown-1", "pkg-unknown-2"})
	assert.Empty(t, effective)
	assert.Empty(t, configs)
}

func TestResolveRequest_NilServiceReturnsEmpty(t *testing.T) {
	effective, configs := ResolveRequest(nil, "https://seller.example/agent", []string{"pkg-1"})
	assert.Empty(t, effective)
	assert.Empty(t, configs)
}

// A wire seller_agent_url with mixed case, default port, or a dot-segment
// must resolve to the same registration as the canonical form. Regression
// test for the identity-match URL-comparison gap called out in the 3.1.1
// trusted-match audit.
func TestResolveRequest_CanonicalizesSellerAgentURL(t *testing.T) {
	src := newMemorySource()
	r := &targeting.SegmentRule{AnyOf: []string{"a"}}
	// Registration stored with mixed case + explicit default port —
	// canonicalization at write time folds this to
	// "https://seller.example.com/agent".
	src.put("https://Seller.Example.com:443/agent", "pkg-1", r, time.Unix(1, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	variants := []string{
		"https://seller.example.com/agent",
		"https://Seller.Example.com/agent",
		"https://seller.example.com:443/agent",
		"https://seller.example.com/./agent",
	}
	for _, url := range variants {
		effective, configs := ResolveRequest(svc, url, nil)
		assert.Equal(t, []string{"pkg-1"}, effective, url)
		require.Len(t, configs, 1, url)
		assert.Equal(t, r, configs["pkg-1"].TargetSegments, url)
	}
}
