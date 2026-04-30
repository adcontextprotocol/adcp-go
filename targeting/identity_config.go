package targeting

import (
	"context"
	"encoding/json"
	"fmt"
)

// PackageIdentityConfig is the identity-side configuration for a package,
// stored in the Store as JSON at key "config:pkg:{packageID}".
type PackageIdentityConfig struct {
	TargetSegments []string `json:"target_segments,omitempty"`
}

// loadPackageIdentityConfig reads identity config for a package from the Store.
// Returns nil if no config is found (package has no identity dimensions).
func loadPackageIdentityConfig(ctx context.Context, store Store, pkgID string) (*PackageIdentityConfig, error) {
	key := fmt.Sprintf("config:pkg:%s", pkgID)
	val, ok, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	var cfg PackageIdentityConfig
	if err := json.Unmarshal([]byte(val), &cfg); err != nil {
		return nil, fmt.Errorf("parse identity config for %s: %w", pkgID, err)
	}
	return &cfg, nil
}

// batchLoadPackageContextConfigs loads context configs for multiple packages in one MGet.
func batchLoadPackageContextConfigs(ctx context.Context, store Store, pkgIDs []string) (map[string]*PackageContextConfig, error) {
	if len(pkgIDs) == 0 {
		return nil, nil
	}
	keys := make([]string, len(pkgIDs))
	for i, id := range pkgIDs {
		keys[i] = fmt.Sprintf("config:pkg:%s:context", id)
	}
	values, err := store.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*PackageContextConfig, len(pkgIDs))
	for i, val := range values {
		if val == "" {
			continue
		}
		var cfg PackageContextConfig
		if err := json.Unmarshal([]byte(val), &cfg); err != nil {
			continue
		}
		result[pkgIDs[i]] = &cfg
	}
	return result, nil
}

// batchLoadPackageIdentityConfigs loads identity configs for multiple packages in one MGet.
func batchLoadPackageIdentityConfigs(ctx context.Context, store Store, pkgIDs []string) (map[string]*PackageIdentityConfig, error) {
	if len(pkgIDs) == 0 {
		return nil, nil
	}
	keys := make([]string, len(pkgIDs))
	for i, id := range pkgIDs {
		keys[i] = fmt.Sprintf("config:pkg:%s", id)
	}
	values, err := store.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*PackageIdentityConfig, len(pkgIDs))
	for i, val := range values {
		if val == "" {
			continue
		}
		var cfg PackageIdentityConfig
		if err := json.Unmarshal([]byte(val), &cfg); err != nil {
			continue
		}
		result[pkgIDs[i]] = &cfg
	}
	return result, nil
}

// SeedPackageIdentityConfig writes identity config for a package to any Store.
func SeedPackageIdentityConfig(ctx context.Context, store Store, pkgID string, cfg PackageIdentityConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return store.Set(ctx, fmt.Sprintf("config:pkg:%s", pkgID), string(data), 0)
}
