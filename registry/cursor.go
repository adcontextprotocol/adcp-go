package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// CursorStore persists the feed cursor across restarts.
type CursorStore interface {
	Load(ctx context.Context) (string, error)
	Save(ctx context.Context, cursor string) error
}

// FileCursorStore persists the cursor to a file. Writes are atomic
// (write tmp + rename) to prevent corruption on crash.
type FileCursorStore struct {
	path string
}

// NewFileCursorStore creates a file-backed cursor store.
func NewFileCursorStore(path string) *FileCursorStore {
	return &FileCursorStore{path: path}
}

func (f *FileCursorStore) Load(_ context.Context) (string, error) {
	data, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (f *FileCursorStore) Save(_ context.Context, cursor string) error {
	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, ".cursor-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.WriteString(cursor); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, f.path)
}

// MemoryCursorStore is an in-memory implementation for testing.
type MemoryCursorStore struct {
	mu     sync.Mutex
	cursor string
}

func (m *MemoryCursorStore) Load(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursor, nil
}

func (m *MemoryCursorStore) Save(_ context.Context, cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursor = cursor
	return nil
}
