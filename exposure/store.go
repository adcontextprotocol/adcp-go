package exposure

import (
	"context"
	"time"
)

// ConfigStore is the narrow Store surface required for reading and seeding
// package/campaign configuration.
type ConfigStore interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

// BatchStore is the narrow Store surface required for batch-loading configs.
type BatchStore interface {
	MGet(ctx context.Context, keys ...string) ([]string, error)
}

// RecorderStore is the Store surface required by ExposureRecorder.
//
// Any backing store that supports string reads/writes, sorted-set append
// with TTL, and config reads satisfies this interface. targeting.Store and
// concrete Valkey/in-memory implementations satisfy it structurally — no
// explicit conformance is required.
type RecorderStore interface {
	ConfigStore
	ZAdd(ctx context.Context, key string, score float64, member string) error
	ZRemRangeByScore(ctx context.Context, key string, min, max float64) error
	ZExpire(ctx context.Context, key string, ttl time.Duration) error
}
