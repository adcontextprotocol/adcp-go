package targeting

import (
	"context"
	"encoding/json"
	"fmt"
)

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
			continue // skip unparseable
		}
		result[pkgIDs[i]] = &cfg
	}
	return result, nil
}
