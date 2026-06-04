// Package pkgconfigstore owns the storage layout for per-package
// context-side configuration. Writers (governance pipelines, media-buy
// sync) import Service to put / remove configs; the context agent reads
// through Reader, optionally wrapped by an LRU cache decorator.
//
// Storage key:
//
//   - "config:pkg:{package_id}:context" → JSON PackageContextConfig
package pkgconfigstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
)

// Store is the minimal Valkey surface this package consumes.
type Store interface {
	Get(ctx context.Context, key string) (string, bool, error)
	MGet(ctx context.Context, keys ...string) ([]string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Del(ctx context.Context, keys ...string) error
}

// Key returns the Valkey key holding the JSON payload for a package's
// context-side configuration. Exported so writers / readers that bypass
// the Service share the key shape.
func Key(packageID string) string {
	return "config:pkg:" + packageID + ":context"
}

// Service is the write surface for per-package context configs.
type Service struct {
	store Store
}

// NewService constructs a Service backed by store.
func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("pkgconfigstore: store is required")
	}
	return &Service{store: store}, nil
}

// Put writes (or replaces) one package's context config.
func (s *Service) Put(ctx context.Context, cfg *targeting.PackageContextConfig) error {
	if cfg == nil {
		return errors.New("pkgconfigstore: config is required")
	}
	if cfg.PackageID == "" {
		return errors.New("pkgconfigstore: package_id is required")
	}
	payload, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("pkgconfigstore: marshal: %w", err)
	}
	if err := s.store.Set(ctx, Key(cfg.PackageID), string(payload), 0); err != nil {
		return fmt.Errorf("pkgconfigstore: persist %q: %w", cfg.PackageID, err)
	}
	return nil
}

// Remove deletes one package's context config. Missing records are a
// no-op.
func (s *Service) Remove(ctx context.Context, packageID string) error {
	if packageID == "" {
		return errors.New("pkgconfigstore: package_id is required")
	}
	return s.store.Del(ctx, Key(packageID))
}

// Reader is the read surface the context engine consumes.
type Reader interface {
	// Get returns the config for one package. ok == false (with err == nil)
	// means no config is stored.
	Get(ctx context.Context, packageID string) (cfg *targeting.PackageContextConfig, ok bool, err error)
}

// NewReader returns a direct Reader that issues one Valkey round-trip
// per call.
func NewReader(store Store) Reader {
	return &reader{store: store}
}

type reader struct {
	store Store
}

func (r *reader) Get(ctx context.Context, packageID string) (*targeting.PackageContextConfig, bool, error) {
	if packageID == "" {
		return nil, false, errors.New("pkgconfigstore: package_id is required")
	}
	raw, ok, err := r.store.Get(ctx, Key(packageID))
	if err != nil {
		return nil, false, err
	}
	if !ok || raw == "" {
		return nil, false, nil
	}
	var cfg targeting.PackageContextConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, false, fmt.Errorf("pkgconfigstore: decode %q: %w", packageID, err)
	}
	return &cfg, true, nil
}
