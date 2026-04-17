package exposure

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is a minimal in-memory Store implementation used by Recorder
// tests. It satisfies the Store interface without importing any targeting
// machinery — the whole point of this test is to demonstrate that a caller
// can wire Recorder against a bare Store with zero targeting coupling.
type fakeStore struct {
	mu      sync.Mutex
	strings map[string]string
	zsets   map[string][]zmember
}

type zmember struct {
	score  float64
	member string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		strings: make(map[string]string),
		zsets:   make(map[string][]zmember),
	}
}

func (s *fakeStore) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.strings[key]
	return v, ok, nil
}

func (s *fakeStore) Set(_ context.Context, key, value string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strings[key] = value
	return nil
}

func (s *fakeStore) ZAdd(_ context.Context, key string, score float64, member string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.zsets[key] = append(s.zsets[key], zmember{score, member})
	return nil
}

func (s *fakeStore) ZRemRangeByScore(_ context.Context, _ string, _, _ float64) error {
	return nil
}

func (s *fakeStore) ZExpire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

// TestStandaloneRecorder_MinimalWiring verifies the motivating use case for
// splitting Recorder into its own package: a caller can construct a Recorder
// from just (Store, ProviderID), call RecordExposure, and observe the full
// store-schema writes — with no Engine, property registry, signature
// verification, or package configuration anywhere in sight.
func TestStandaloneRecorder_MinimalWiring(t *testing.T) {
	store := newFakeStore()
	recorder := NewRecorder(RecorderConfig{
		ProviderID: "standalone-provider",
		Store:      store,
	})

	ctx := context.Background()
	resp, err := recorder.RecordExposure(ctx, &ExposeRequest{
		UserToken:    "user-standalone",
		PackageID:    "pkg-standalone",
		ImpressionID: "imp-standalone-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "pkg-standalone", resp.PackageID)

	// Binary exposure log written under user:exposures:<hash>.
	userHash := HashToken("user-standalone")
	val, ok, err := store.Get(ctx, "user:exposures:"+userHash)
	require.NoError(t, err)
	require.True(t, ok, "expected user:exposures:* entry")

	blog := BinaryExposureLog(val)
	require.NoError(t, ValidateBinaryLog(blog))
	require.Equal(t, 1, blog.Len(), "expected one entry in the binary log")
	assert.Equal(t, HashString("pkg-standalone"), blog.PackageHash(0))
	assert.Equal(t, HashString("imp-standalone-1"), blog.ImpressionHash(0))

	// Source hash falls back to ProviderID when SourceID is not set on the request.
	assert.Equal(t, HashString("standalone-provider"), blog.SourceHash(0),
		"expected source hash to fall back to ProviderID")

	// Package-level frequency sorted-set received the impression.
	pkgFreqKey := "freq:pkg:pkg-standalone:" + userHash
	_, hasFreq := store.zsets[pkgFreqKey]
	assert.True(t, hasFreq, "expected freq:pkg:* sorted-set entry")

	// Intent timestamp under intent:pkg-standalone:<hash>.
	_, ok, err = store.Get(ctx, "intent:pkg-standalone:"+userHash)
	require.NoError(t, err)
	assert.True(t, ok, "expected intent:* entry")

	// No campaign frequency set — the request had no CampaignID and the
	// standalone store was not pre-seeded with a PackageIdentityConfig.
	// CampaignCount/CampaignRemaining should both be zero on the response.
	assert.Zero(t, resp.CampaignCount)
	assert.Zero(t, resp.CampaignRemaining)
}

// TestStandaloneRecorder_SharedClock verifies that a single Clock shared
// between the recorder and its caller drives time consistently. This is the
// follow-up to the PR review note on the dual-Now papercut.
func TestStandaloneRecorder_SharedClock(t *testing.T) {
	store := newFakeStore()

	fixedTime := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	clock := ClockFunc(func() time.Time { return fixedTime })

	recorder := NewRecorder(RecorderConfig{
		ProviderID: "clock-provider",
		Store:      store,
		Clock:      clock,
	})

	_, err := recorder.RecordExposure(context.Background(), &ExposeRequest{
		UserToken: "user-clock",
		PackageID: "pkg-clock",
	})
	require.NoError(t, err)

	// The entry timestamp must reflect the injected clock, not wall time.
	val, _, _ := store.Get(context.Background(), "user:exposures:"+HashToken("user-clock"))
	blog := BinaryExposureLog(val)
	require.Equal(t, 1, blog.Len())
	assert.Equal(t, fixedTime.Unix(), blog.Timestamp(0))
}
