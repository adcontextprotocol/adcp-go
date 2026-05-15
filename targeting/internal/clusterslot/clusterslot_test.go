package clusterslot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashTag(t *testing.T) {
	cases := []struct {
		key, want string
	}{
		{"plain", "plain"},
		{"", ""},
		{"{tag}rest", "tag"},
		{"prefix{tag}suffix", "tag"},
		{"{}empty", "{}empty"},
		{"{unterminated", "{unterminated"},
		{"no-open}close", "no-open}close"},
		{"{a}{b}", "a"},
		{"{", "{"},
		{"a{", "a{"},
		{"}", "}"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, hashTag(tc.key), "hashTag(%q)", tc.key)
	}
}

func TestSlot(t *testing.T) {
	assert.Equal(t, uint16(0x31C3), crc16("123456789"))
	assert.Equal(t, 12739, Slot("123456789"))
	assert.Equal(t, 0, Slot(""))
	assert.Equal(t, Slot("foo"), Slot("{foo}bar"))
	assert.NotEqual(t, Slot("foo"), Slot("{}foo"))
}

// TestShardMap_BoundariesMatchValkeyCli verifies that the per-shard
// last-slot boundaries match the slot allocation `valkey-cli --cluster
// create` produces. Reference values were captured by running
// `valkey-cli --cluster create` against valkey:9 with N nodes and
// reading CLUSTER SLOTS.
func TestShardMap_BoundariesMatchValkeyCli(t *testing.T) {
	cases := []struct {
		n        int
		lastSlot []int
	}{
		{1, []int{16383}},
		{2, []int{8191, 16383}},
		{3, []int{5460, 10922, 16383}},
		{4, []int{4095, 8191, 12287, 16383}},
		{5, []int{3276, 6553, 9829, 13106, 16383}},
		{6, []int{2730, 5460, 8191, 10922, 13652, 16383}},
		{8, []int{2047, 4095, 6143, 8191, 10239, 12287, 14335, 16383}},
	}
	for _, tc := range cases {
		m := NewShardMap(tc.n)
		assert.Equalf(t, tc.lastSlot, m.LastSlots(), "n=%d", tc.n)
		assert.Equal(t, tc.n, m.NumShards())
	}
}

func TestShardMap_SingleShardAbsorbsEverything(t *testing.T) {
	m := NewShardMap(1)
	for _, k := range []string{"", "a", "fcap:123", "audience:user:abc"} {
		assert.Equal(t, 0, m.Shard(k), "single shard must absorb key %q", k)
	}
}

func TestShardMap_ShardInRange(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 8} {
		m := NewShardMap(n)
		for s := 0; s < Total; s++ {
			got := lookup(m, s)
			require.Truef(t, got >= 0 && got < n, "n=%d slot=%d returned shard %d", n, s, got)
		}
	}
}

func TestShardMap_BoundaryAdjacentSlots(t *testing.T) {
	// For N=3, slot 10922 owned by shard 1, slot 10923 owned by shard 2.
	// This is the off-by-one zone where a naive integer-division mapping
	// would mis-route slot 10922 to shard 2.
	m := NewShardMap(3)
	assert.Equal(t, 1, lookup(m, 10922), "slot 10922 belongs to shard 1 per valkey-cli")
	assert.Equal(t, 2, lookup(m, 10923), "slot 10923 belongs to shard 2 per valkey-cli")
	assert.Equal(t, 0, lookup(m, 5460), "slot 5460 belongs to shard 0")
	assert.Equal(t, 1, lookup(m, 5461), "slot 5461 belongs to shard 1")
}

func lookup(m *ShardMap, slot int) int {
	return m.ShardForSlot(slot)
}

func TestShard_DelegatesToShardMap(t *testing.T) {
	assert.Equal(t, NewShardMap(3).Shard("fcap:abc"), Shard("fcap:abc", 3))
}
