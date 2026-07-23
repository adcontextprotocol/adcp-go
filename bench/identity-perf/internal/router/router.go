// Package router dispatches writes/reads to the correct Valkey backend
// for a given key. It mirrors the identity-agent's redisstore routing so
// bench tools (seeder, writer) land keys on the exact shards the agent
// reads from — one type serves all three deployment topologies:
//
//	standalone: one client (Shards["0"]).
//	cluster:    one ClusterClient across all shards; go-redis routes
//	            per key via CLUSTER SLOTS.
//	shadow:     N standalone clients; picks the shard client by
//	            CRC16(hashtag(key)) → valkey-cli-style ordinal.
//
// The shadow-mode shard map is a copy of targeting/internal/clusterslot;
// keep them in lockstep.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Router is safe for concurrent use.
type Router struct {
	Mode   string
	sm     *shardMap
	shard  []*redis.Client
	one    redis.UniversalClient
}

// New constructs a Router from `${prefix}_VALKEY_*` env vars, matching
// the identity-agent's Config.
func New(ctx context.Context, prefix string) (*Router, error) {
	mode := envStr(prefix+"_VALKEY_MODE", "standalone")
	shardsRaw := envStr(prefix+"_VALKEY_SHARDS", "")
	if shardsRaw == "" {
		return nil, fmt.Errorf("%s_VALKEY_SHARDS is required", prefix)
	}
	addrs := map[string]string{}
	if err := json.Unmarshal([]byte(shardsRaw), &addrs); err != nil {
		return nil, fmt.Errorf("%s_VALKEY_SHARDS: %w", prefix, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s_VALKEY_SHARDS has no entries", prefix)
	}
	poolSize := envInt(prefix+"_VALKEY_POOL_SIZE", 64)
	username := envStr(prefix+"_VALKEY_USERNAME", "")
	password := envStr(prefix+"_VALKEY_PASSWORD", "")
	db := envInt(prefix+"_VALKEY_DB", 0)

	r := &Router{Mode: mode}
	switch mode {
	case "standalone":
		addr, ok := addrs["0"]
		if !ok {
			return nil, fmt.Errorf("%s: mode=standalone requires shards[0]", prefix)
		}
		r.one = redis.NewClient(&redis.Options{
			Addr: addr, Username: username, Password: password, DB: db, PoolSize: poolSize,
		})
	case "cluster":
		r.one = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs: sortedAddrs(addrs), Username: username, Password: password, PoolSize: poolSize,
		})
	case "shadow":
		ordinals := make([]int, 0, len(addrs))
		for k := range addrs {
			n, err := strconv.Atoi(k)
			if err != nil {
				return nil, fmt.Errorf("%s: shadow shard key %q is not an integer", prefix, k)
			}
			ordinals = append(ordinals, n)
		}
		sort.Ints(ordinals)
		r.shard = make([]*redis.Client, len(ordinals))
		for i, ord := range ordinals {
			if ord != i {
				return nil, fmt.Errorf("%s: shadow ordinals must be contiguous 0..%d, got %v",
					prefix, len(ordinals)-1, ordinals)
			}
			r.shard[i] = redis.NewClient(&redis.Options{
				Addr: addrs[strconv.Itoa(ord)], Username: username, Password: password,
				DB: db, PoolSize: poolSize,
			})
		}
		r.sm = newShardMap(len(r.shard))
	default:
		return nil, fmt.Errorf("%s: unsupported mode %q", prefix, mode)
	}

	if err := r.WaitReady(ctx, 2*time.Minute); err != nil {
		return nil, err
	}
	return r, nil
}

// NumShards returns 1 for standalone/cluster (single client) and N for
// shadow.
func (r *Router) NumShards() int {
	if r.Mode == "shadow" {
		return len(r.shard)
	}
	return 1
}

// ClientFor returns the destination for key. Cluster mode returns the
// shared ClusterClient (which does its own routing); shadow mode picks
// the per-shard client by CRC16.
func (r *Router) ClientFor(key string) redis.UniversalClient {
	if r.Mode == "shadow" {
		return r.shard[r.sm.shard(key)]
	}
	return r.one
}

// WaitReady blocks until every underlying client answers PING (or timeout).
func (r *Router) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	pingOne := func(c redis.UniversalClient, label string) error {
		var last error
		for time.Now().Before(deadline) {
			if err := c.Ping(ctx).Err(); err == nil {
				return nil
			} else {
				last = err
			}
			time.Sleep(500 * time.Millisecond)
		}
		return fmt.Errorf("%s: not ready after %s: %w", label, timeout, last)
	}
	if r.one != nil {
		if err := pingOne(r.one, r.Mode); err != nil {
			return err
		}
	}
	for i, c := range r.shard {
		if err := pingOne(c, fmt.Sprintf("shadow-%d", i)); err != nil {
			return err
		}
	}
	return nil
}

// FlushAll clears every backend keyspace. Cluster mode fans out to every
// master; shadow mode iterates each shadow.
func (r *Router) FlushAll(ctx context.Context) error {
	switch r.Mode {
	case "standalone":
		return r.one.FlushDB(ctx).Err()
	case "cluster":
		return r.one.(*redis.ClusterClient).ForEachMaster(ctx, func(ctx context.Context, c *redis.Client) error {
			return c.FlushDB(ctx).Err()
		})
	case "shadow":
		for i, c := range r.shard {
			if err := c.FlushDB(ctx).Err(); err != nil {
				return fmt.Errorf("shard %d flush: %w", i, err)
			}
		}
		return nil
	}
	return fmt.Errorf("unsupported mode %q", r.Mode)
}

// Close releases every underlying client. Safe to call multiple times.
func (r *Router) Close() error {
	var errs []error
	if r.one != nil {
		if err := r.one.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, c := range r.shard {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func sortedAddrs(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, ea := strconv.Atoi(keys[i])
		b, eb := strconv.Atoi(keys[j])
		if ea == nil && eb == nil {
			return a < b
		}
		return keys[i] < keys[j]
	})
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = m[k]
	}
	return out
}

// --- shardMap: CRC16 + valkey-cli-style slot-to-ordinal mapping ---
// Copy of targeting/internal/clusterslot (internal to that subtree, can't
// import from bench/identity-perf/). Keep in lockstep.

const slotTotal = 16384

type shardMap struct {
	lastSlot []int
}

func newShardMap(numShards int) *shardMap {
	if numShards <= 1 {
		return &shardMap{lastSlot: []int{slotTotal - 1}}
	}
	last := make([]int, numShards)
	slotsPerNode := float64(slotTotal) / float64(numShards)
	cursor := 0.0
	first := 0
	for i := range numShards {
		end := int(math.Round(cursor + slotsPerNode - 1))
		if end > slotTotal-1 || i == numShards-1 {
			end = slotTotal - 1
		}
		if end < first {
			end = first
		}
		last[i] = end
		first = end + 1
		cursor += slotsPerNode
	}
	return &shardMap{lastSlot: last}
}

func (m *shardMap) shard(key string) int {
	if len(m.lastSlot) <= 1 {
		return 0
	}
	slot := int(crc16(hashTag(key))) % slotTotal
	for i, end := range m.lastSlot {
		if slot <= end {
			return i
		}
	}
	return len(m.lastSlot) - 1
}

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

func crc16(s string) uint16 {
	var crc uint16
	for i := 0; i < len(s); i++ {
		crc = (crc << 8) ^ crc16tab[byte(crc>>8)^s[i]]
	}
	return crc
}

// CRC-16/XMODEM (CCITT) table. Matches Valkey's src/crc16.c.
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

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
