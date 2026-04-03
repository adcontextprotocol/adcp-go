package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCursorStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor")
	store := NewFileCursorStore(path)
	ctx := context.Background()

	// Load from missing file returns empty
	cursor, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cursor != "" {
		t.Errorf("cursor = %q, want empty", cursor)
	}

	// Save and reload
	if err := store.Save(ctx, "019414a0-0000-7000-0000-000000000001"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cursor, err = store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cursor != "019414a0-0000-7000-0000-000000000001" {
		t.Errorf("cursor = %q", cursor)
	}

	// Overwrite
	if err := store.Save(ctx, "019414a0-0000-7000-0000-000000000002"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cursor, _ = store.Load(ctx)
	if cursor != "019414a0-0000-7000-0000-000000000002" {
		t.Errorf("cursor = %q", cursor)
	}
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
		if e.Name() != "cursor" {
			t.Errorf("unexpected file: %s", e.Name())
		}
	}
}

func TestMemoryCursorStore(t *testing.T) {
	store := &MemoryCursorStore{}
	ctx := context.Background()

	cursor, _ := store.Load(ctx)
	if cursor != "" {
		t.Errorf("initial = %q", cursor)
	}

	_ = store.Save(ctx, "abc")
	cursor, _ = store.Load(ctx)
	if cursor != "abc" {
		t.Errorf("cursor = %q", cursor)
	}
}
