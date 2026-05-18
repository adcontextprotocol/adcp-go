package redisstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing mode",
			cfg:     Config{Shards: map[string]string{"0": "h:1"}},
			wantErr: "invalid mode",
		},
		{
			name:    "empty shards",
			cfg:     Config{Mode: ModeStandalone},
			wantErr: "shards must contain at least one entry",
		},
		{
			name:    "shard value empty",
			cfg:     Config{Mode: ModeStandalone, Shards: map[string]string{"0": ""}},
			wantErr: `shards["0"] is empty`,
		},
		{
			name:    "standalone without 0 key",
			cfg:     Config{Mode: ModeStandalone, Shards: map[string]string{"1": "h:1"}},
			wantErr: `mode=standalone requires shards to contain key "0"`,
		},
		{
			name: "shadow ordinals not contiguous",
			cfg: Config{Mode: ModeShadow, Shards: map[string]string{
				"0": "h:1", "2": "h:2",
			}},
			wantErr: "shard ordinals must be contiguous",
		},
		{
			name:    "shadow non-integer ordinal",
			cfg:     Config{Mode: ModeShadow, Shards: map[string]string{"foo": "h:1"}},
			wantErr: "not an integer ordinal",
		},
		{
			name: "ok standalone",
			cfg:  Config{Mode: ModeStandalone, Shards: map[string]string{"0": "h:1"}},
		},
		{
			name: "ok cluster",
			cfg:  Config{Mode: ModeCluster, Shards: map[string]string{"0": "h:1", "1": "h:2"}},
		},
		{
			name: "ok shadow",
			cfg:  Config{Mode: ModeShadow, Shards: map[string]string{"0": "h:1", "1": "h:2"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSortedValues(t *testing.T) {
	got := sortedValues(map[string]string{"2": "c", "0": "a", "10": "d", "1": "b"})
	assert.Equal(t, []string{"a", "b", "c", "d"}, got)
}
