// Package urlliststore owns the storage layout for per-package URL
// blocklists and allowlists. Writers (governance pipelines) import
// Service to add / remove hashes; the context agent reads through
// Reader, optionally wrapped by an LRU cache decorator.
//
// Storage keys:
//
//   - "url:blocklist:{package_id}" → SET of URL hashes (caller hashes
//     via targeting.HashURL — see that godoc for the interop contract)
//   - "url:allowlist:{package_id}" → SET of URL hashes
package urlliststore

import (
	"context"
	"errors"
)

// Store is the minimal Valkey surface this package consumes.
type Store interface {
	SetIsMember(ctx context.Context, key, member string) (bool, error)
	SetMembers(ctx context.Context, key string) ([]string, error)
	SetAdd(ctx context.Context, key string, members ...string) error
	SetRemove(ctx context.Context, key string, members ...string) error
	Del(ctx context.Context, keys ...string) error
}

// BlocklistKey returns the Valkey key holding the blocklist set for a
// package.
func BlocklistKey(packageID string) string { return "url:blocklist:" + packageID }

// AllowlistKey returns the Valkey key holding the allowlist set for a
// package.
func AllowlistKey(packageID string) string { return "url:allowlist:" + packageID }

// Service is the write surface for per-package URL lists. Writers
// supply already-hashed values (via targeting.HashURL); the package
// itself is hash-agnostic.
type Service struct {
	store Store
}

// NewService constructs a Service backed by store.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("urlliststore: store is required")
	}
	return &Service{store: store}, nil
}

// AddToBlocklist adds one or more hashes to packageID's blocklist.
func (s *Service) AddToBlocklist(ctx context.Context, packageID string, hashes ...string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	if len(hashes) == 0 {
		return nil
	}
	return s.store.SetAdd(ctx, BlocklistKey(packageID), hashes...)
}

// RemoveFromBlocklist drops one or more hashes from packageID's
// blocklist.
func (s *Service) RemoveFromBlocklist(ctx context.Context, packageID string, hashes ...string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	if len(hashes) == 0 {
		return nil
	}
	return s.store.SetRemove(ctx, BlocklistKey(packageID), hashes...)
}

// ClearBlocklist deletes the blocklist for packageID.
func (s *Service) ClearBlocklist(ctx context.Context, packageID string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	return s.store.Del(ctx, BlocklistKey(packageID))
}

// AddToAllowlist adds one or more hashes to packageID's allowlist.
func (s *Service) AddToAllowlist(ctx context.Context, packageID string, hashes ...string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	if len(hashes) == 0 {
		return nil
	}
	return s.store.SetAdd(ctx, AllowlistKey(packageID), hashes...)
}

// RemoveFromAllowlist drops one or more hashes from packageID's
// allowlist.
func (s *Service) RemoveFromAllowlist(ctx context.Context, packageID string, hashes ...string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	if len(hashes) == 0 {
		return nil
	}
	return s.store.SetRemove(ctx, AllowlistKey(packageID), hashes...)
}

// ClearAllowlist deletes the allowlist for packageID.
func (s *Service) ClearAllowlist(ctx context.Context, packageID string) error {
	if packageID == "" {
		return errors.New("urlliststore: package_id is required")
	}
	return s.store.Del(ctx, AllowlistKey(packageID))
}

// Reader is the read surface the context engine consumes.
type Reader interface {
	IsBlocked(ctx context.Context, packageID, urlHash string) (bool, error)
	IsAllowed(ctx context.Context, packageID, urlHash string) (bool, error)
}

// NewReader returns a direct Reader that issues one SISMEMBER per call.
func NewReader(store Store) Reader {
	return &reader{store: store}
}

type reader struct {
	store Store
}

func (r *reader) IsBlocked(ctx context.Context, packageID, urlHash string) (bool, error) {
	if packageID == "" || urlHash == "" {
		return false, nil
	}
	return r.store.SetIsMember(ctx, BlocklistKey(packageID), urlHash)
}

func (r *reader) IsAllowed(ctx context.Context, packageID, urlHash string) (bool, error) {
	if packageID == "" || urlHash == "" {
		return false, nil
	}
	return r.store.SetIsMember(ctx, AllowlistKey(packageID), urlHash)
}
