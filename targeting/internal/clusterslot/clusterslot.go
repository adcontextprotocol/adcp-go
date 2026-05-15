// Package clusterslot implements the Valkey/Redis Cluster CRC16 slot
// algorithm and the slot-to-shard-ordinal mapping that adcp-go Stores
// use when fanning out reads across independent standalone replicas
// (shadow-replica mode).
//
// The slot algorithm is the same one the cluster protocol uses, so an
// app-level CRC16 here selects the same slot a cluster-aware client
// would route to.
//
// The slot-to-shard mapping mirrors the slot allocation produced by
// `valkey-cli --cluster create` for N primaries. The reference
// implementation in valkey/src/valkey-cli.c walks a floating-point
// cursor (`slots_per_node = 16384 / N`) and rounds to the nearest
// integer on each step (`lround(cursor + slots_per_node - 1)`), with
// the last primary clamped to slot 16383. ShardMap precomputes the
// resulting boundaries so per-key lookups are a short linear scan.
package clusterslot

import (
	"math"
	"strings"
)

// Total is the size of the Valkey/Redis Cluster slot space.
const Total = 16384

// Slot returns the cluster slot for key, applying the standard hashtag
// rule (a substring between the first '{' and the next non-empty '}'
// is hashed in place of the full key). Empty keys hash to slot 0.
func Slot(key string) int {
	return int(crc16(hashTag(key))) % Total
}

// ShardMap routes keys to shard ordinals using the slot allocation
// `valkey-cli --cluster create` produces for a fixed number of primary
// shards. Construct once and reuse — boundary computation is O(N) and
// per-key lookups are O(N) over a small slice.
type ShardMap struct {
	// lastSlot[i] is the highest slot owned by shard ordinal i.
	lastSlot []int
}

// NewShardMap precomputes shard boundaries for numShards primary nodes.
// numShards <= 1 produces a map that routes every key to shard 0.
func NewShardMap(numShards int) *ShardMap {
	if numShards <= 1 {
		return &ShardMap{lastSlot: []int{Total - 1}}
	}
	last := make([]int, numShards)
	slotsPerNode := float64(Total) / float64(numShards)
	cursor := 0.0
	first := 0
	for i := 0; i < numShards; i++ {
		end := int(math.Round(cursor + slotsPerNode - 1))
		if end > Total-1 || i == numShards-1 {
			end = Total - 1
		}
		if end < first {
			end = first
		}
		last[i] = end
		first = end + 1
		cursor += slotsPerNode
	}
	return &ShardMap{lastSlot: last}
}

// NumShards reports the number of shards this map routes across.
func (m *ShardMap) NumShards() int { return len(m.lastSlot) }

// LastSlots returns the highest slot each shard ordinal owns. The
// returned slice is a defensive copy.
func (m *ShardMap) LastSlots() []int {
	out := make([]int, len(m.lastSlot))
	copy(out, m.lastSlot)
	return out
}

// Shard returns the destination shard ordinal for key. Always in
// [0, NumShards()).
func (m *ShardMap) Shard(key string) int {
	if len(m.lastSlot) <= 1 {
		return 0
	}
	return m.ShardForSlot(Slot(key))
}

// ShardForSlot returns the destination shard ordinal for slot. Useful
// when slot is known directly (e.g., from CLUSTER SLOTS) and CRC16
// inversion would be wasteful.
func (m *ShardMap) ShardForSlot(slot int) int {
	if len(m.lastSlot) <= 1 {
		return 0
	}
	for i, end := range m.lastSlot {
		if slot <= end {
			return i
		}
	}
	return len(m.lastSlot) - 1
}

// Shard is a convenience for one-off lookups. For repeated lookups,
// build a ShardMap once with NewShardMap and reuse it.
func Shard(key string, numShards int) int {
	return NewShardMap(numShards).Shard(key)
}

// hashTag returns the substring between the first '{' and the next '}'
// if non-empty; otherwise it returns key.
func hashTag(key string) string {
	s := strings.IndexByte(key, '{')
	if s < 0 {
		return key
	}
	e := strings.IndexByte(key[s+1:], '}')
	if e <= 0 {
		return key
	}
	return key[s+1 : s+1+e]
}

// crc16 implements CRC-16/XMODEM (CCITT), the algorithm Valkey Cluster
// uses for slot assignment.
func crc16(s string) uint16 {
	var crc uint16
	for i := 0; i < len(s); i++ {
		crc = (crc << 8) ^ crc16tab[byte(crc>>8)^s[i]]
	}
	return crc
}

var crc16tab = [256]uint16{
	0x0000, 0x1021, 0x2042, 0x3063, 0x4084, 0x50a5, 0x60c6, 0x70e7,
	0x8108, 0x9129, 0xa14a, 0xb16b, 0xc18c, 0xd1ad, 0xe1ce, 0xf1ef,
	0x1231, 0x0210, 0x3273, 0x2252, 0x52b5, 0x4294, 0x72f7, 0x62d6,
	0x9339, 0x8318, 0xb37b, 0xa35a, 0xd3bd, 0xc39c, 0xf3ff, 0xe3de,
	0x2462, 0x3443, 0x0420, 0x1401, 0x64e6, 0x74c7, 0x44a4, 0x5485,
	0xa56a, 0xb54b, 0x8528, 0x9509, 0xe5ee, 0xf5cf, 0xc5ac, 0xd58d,
	0x3653, 0x2672, 0x1611, 0x0630, 0x76d7, 0x66f6, 0x5695, 0x46b4,
	0xb75b, 0xa77a, 0x9719, 0x8738, 0xf7df, 0xe7fe, 0xd79d, 0xc7bc,
	0x48c4, 0x58e5, 0x6886, 0x78a7, 0x0840, 0x1861, 0x2802, 0x3823,
	0xc9cc, 0xd9ed, 0xe98e, 0xf9af, 0x8948, 0x9969, 0xa90a, 0xb92b,
	0x5af5, 0x4ad4, 0x7ab7, 0x6a96, 0x1a71, 0x0a50, 0x3a33, 0x2a12,
	0xdbfd, 0xcbdc, 0xfbbf, 0xeb9e, 0x9b79, 0x8b58, 0xbb3b, 0xab1a,
	0x6ca6, 0x7c87, 0x4ce4, 0x5cc5, 0x2c22, 0x3c03, 0x0c60, 0x1c41,
	0xedae, 0xfd8f, 0xcdec, 0xddcd, 0xad2a, 0xbd0b, 0x8d68, 0x9d49,
	0x7e97, 0x6eb6, 0x5ed5, 0x4ef4, 0x3e13, 0x2e32, 0x1e51, 0x0e70,
	0xff9f, 0xefbe, 0xdfdd, 0xcffc, 0xbf1b, 0xaf3a, 0x9f59, 0x8f78,
	0x9188, 0x81a9, 0xb1ca, 0xa1eb, 0xd10c, 0xc12d, 0xf14e, 0xe16f,
	0x1080, 0x00a1, 0x30c2, 0x20e3, 0x5004, 0x4025, 0x7046, 0x6067,
	0x83b9, 0x9398, 0xa3fb, 0xb3da, 0xc33d, 0xd31c, 0xe37f, 0xf35e,
	0x02b1, 0x1290, 0x22f3, 0x32d2, 0x4235, 0x5214, 0x6277, 0x7256,
	0xb5ea, 0xa5cb, 0x95a8, 0x8589, 0xf56e, 0xe54f, 0xd52c, 0xc50d,
	0x34e2, 0x24c3, 0x14a0, 0x0481, 0x7466, 0x6447, 0x5424, 0x4405,
	0xa7db, 0xb7fa, 0x8799, 0x97b8, 0xe75f, 0xf77e, 0xc71d, 0xd73c,
	0x26d3, 0x36f2, 0x0691, 0x16b0, 0x6657, 0x7676, 0x4615, 0x5634,
	0xd94c, 0xc96d, 0xf90e, 0xe92f, 0x99c8, 0x89e9, 0xb98a, 0xa9ab,
	0x5844, 0x4865, 0x7806, 0x6827, 0x18c0, 0x08e1, 0x3882, 0x28a3,
	0xcb7d, 0xdb5c, 0xeb3f, 0xfb1e, 0x8bf9, 0x9bd8, 0xabbb, 0xbb9a,
	0x4a75, 0x5a54, 0x6a37, 0x7a16, 0x0af1, 0x1ad0, 0x2ab3, 0x3a92,
	0xfd2e, 0xed0f, 0xdd6c, 0xcd4d, 0xbdaa, 0xad8b, 0x9de8, 0x8dc9,
	0x7c26, 0x6c07, 0x5c64, 0x4c45, 0x3ca2, 0x2c83, 0x1ce0, 0x0cc1,
	0xef1f, 0xff3e, 0xcf5d, 0xdf7c, 0xaf9b, 0xbfba, 0x8fd9, 0x9ff8,
	0x6e17, 0x7e36, 0x4e55, 0x5e74, 0x2e93, 0x3eb2, 0x0ed1, 0x1ef0,
}
