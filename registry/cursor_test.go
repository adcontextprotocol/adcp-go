package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileCursorStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor")
	store := NewFileCursorStore(path)
	ctx := context.Background()

	// Load from missing file returns empty
	cursor, err := store.Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, cursor)

	// Save and reload
	require.NoError(t, store.Save(ctx, "019414a0-0000-7000-0000-000000000001"))

	cursor, err = store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, "019414a0-0000-7000-0000-000000000001", cursor)

	// Overwrite
	require.NoError(t, store.Save(ctx, "019414a0-0000-7000-0000-000000000002"))
	cursor, _ = store.Load(ctx)
	assert.Equal(t, "019414a0-0000-7000-0000-000000000002", cursor)
}

func TestFileCursorStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor")
	store := NewFileCursorStore(path)
	ctx := context.Background()

	_ = store.Save(ctx, "original")

	// After save, no temp files should remain
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.Equal(t, "cursor", e.Name(), "unexpected file: %s", e.Name())
	}
}

func TestMemoryCursorStore(t *testing.T) {
	store := &MemoryCursorStore{}
	ctx := context.Background()

	cursor, _ := store.Load(ctx)
	assert.Empty(t, cursor, "initial cursor should be empty")

	_ = store.Save(ctx, "abc")
	cursor, _ = store.Load(ctx)
	assert.Equal(t, "abc", cursor)
}
