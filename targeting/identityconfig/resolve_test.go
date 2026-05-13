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
	src := newFakeSource()
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
	src := newFakeSource()
	src.put("seller", "pkg-1", &targeting.SegmentRule{AnyOf: []string{"a"}}, time.Unix(1, 0))
	src.put("seller", "pkg-2", &targeting.SegmentRule{AnyOf: []string{"b"}}, time.Unix(1, 0))

	svc, err := New(src, time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	// pkg-unknown is silently dropped (not registered for this seller).
	effective, configs := ResolveRequest(svc, "seller", []string{"pkg-1", "pkg-unknown"})

	assert.Equal(t, []string{"pkg-1"}, effective)
	require.Len(t, configs, 1)
	require.Contains(t, configs, "pkg-1")
}

func TestResolveRequest_UnknownSellerReturnsEmpty(t *testing.T) {
	svc, err := New(newFakeSource(), time.Hour)
	require.NoError(t, err)
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop()

	effective, configs := ResolveRequest(svc, "https://unknown/agent", []string{"pkg-1"})
	assert.Empty(t, effective)
	assert.Empty(t, configs)
}

func TestResolveRequest_NilServiceReturnsEmpty(t *testing.T) {
	effective, configs := ResolveRequest(nil, "seller", []string{"pkg-1"})
	assert.Empty(t, effective)
	assert.Empty(t, configs)
}
