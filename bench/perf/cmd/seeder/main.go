// seeder populates the audience and fcap Valkey backends with synthetic
// data shaped to what the identity-agent reads at request time.
//
// The seeder honors the same {AUDIENCE,FCAP}_VALKEY_{MODE,SHARDS} env
// vars the identity-agent reads through, so writes land in the same
// place identity-agent's client-side CRC16 (shadow) or CLUSTER SLOTS
// (cluster) reads from.
//
// Key layouts (see targeting/audience and targeting/fcap):
//
//	audience:user:{sha256(user_token)[:16]}  HSET  field=audienceID  value=score
//	fcap:{sha256(user_token)[:16]}           HSET  field="{sellerURL}:{packageID}" value="1"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
	"github.com/redis/go-redis/v9"
)

func main() {
	totalUsers := envInt("TOTAL_USERS", 100_000)
	totalAudiences := envInt("TOTAL_AUDIENCES", 500)
	audiencesPerUser := envInt("AUDIENCES_PER_USER", 5)

	sellerAgentURL := envStr("SELLER_AGENT_URL", "https://seller.perf.local/agent")
	totalPackages := envInt("TOTAL_PACKAGES", 200)
	fcapUserFraction := envFloat("FCAP_USER_FRACTION", 0.5)
	packagesCappedPerUser := envInt("PACKAGES_CAPPED_PER_USER", 10)

	workers := envInt("WORKERS", 32)
	pipelineSize := envInt("PIPELINE_SIZE", 500)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	audienceRouter, err := newRouter(ctx, "AUDIENCE")
	if err != nil {
		log.Fatalf("audience: %v", err)
	}
	defer audienceRouter.Close()
	fcapRouter, err := newRouter(ctx, "FCAP")
	if err != nil {
		log.Fatalf("fcap: %v", err)
	}
	defer fcapRouter.Close()

	log.Printf("flushing audience + fcap stores")
	if err := audienceRouter.FlushAll(ctx); err != nil {
		log.Fatalf("flush audience: %v", err)
	}
	if err := fcapRouter.FlushAll(ctx); err != nil {
		log.Fatalf("flush fcap: %v", err)
	}

	if audiencesPerUser > 0 && totalAudiences > 0 {
		log.Printf("seeding audience: users=%d audiences/user=%d total_audiences=%d topology=%s shards=%d",
			totalUsers, audiencesPerUser, totalAudiences, audienceRouter.mode, audienceRouter.NumShards())
		seedAudience(ctx, audienceRouter, totalUsers, audiencesPerUser, totalAudiences, workers, pipelineSize)
	} else {
		log.Printf("skipping audience seed (AUDIENCES_PER_USER=%d, TOTAL_AUDIENCES=%d)",
			audiencesPerUser, totalAudiences)
	}

	if packagesCappedPerUser > 0 && fcapUserFraction > 0 && totalPackages > 0 {
		log.Printf("seeding fcap: capped_users=%d packages_per_user=%d topology=%s shards=%d",
			int(float64(totalUsers)*fcapUserFraction), packagesCappedPerUser, fcapRouter.mode, fcapRouter.NumShards())
		seedFCap(ctx, fcapRouter, totalUsers, fcapUserFraction, sellerAgentURL, totalPackages, packagesCappedPerUser, workers, pipelineSize)
	} else {
		log.Printf("skipping fcap seed (PACKAGES_CAPPED_PER_USER=%d, FCAP_USER_FRACTION=%g, TOTAL_PACKAGES=%d)",
			packagesCappedPerUser, fcapUserFraction, totalPackages)
	}

	log.Printf("seeder done")
}

// router dispatches writes to the correct backend for a given key,
// matching the identity-agent's redisstore routing per MODE:
//
//	standalone: one client (Shards["0"]).
//	cluster:    one ClusterClient across all shards; go-redis routes
//	            per key via CLUSTER SLOTS.
//	shadow:     N standalone clients; picks the shard client by
//	            CRC16(hashtag(key)) % 16384 → valkey-cli-style ordinal.
type router struct {
	mode  string
	sm    *shardMap        // used only in shadow mode
	shard []*redis.Client  // used only in shadow mode
	one   redis.UniversalClient
}

func newRouter(ctx context.Context, prefix string) (*router, error) {
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

	r := &router{mode: mode}
	switch mode {
	case "standalone":
		addr, ok := addrs["0"]
		if !ok {
			return nil, fmt.Errorf("%s: mode=standalone requires shards[0]", prefix)
		}
		r.one = redis.NewClient(&redis.Options{
			Addr:     addr,
			Username: username,
			Password: password,
			DB:       db,
			PoolSize: poolSize,
		})
	case "cluster":
		r.one = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    sortedAddrs(addrs),
			Username: username,
			Password: password,
			PoolSize: poolSize,
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
				return nil, fmt.Errorf("%s: shadow ordinals must be contiguous 0..%d, got %v", prefix, len(ordinals)-1, ordinals)
			}
			r.shard[i] = redis.NewClient(&redis.Options{
				Addr:     addrs[strconv.Itoa(ord)],
				Username: username,
				Password: password,
				DB:       db,
				PoolSize: poolSize,
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

func (r *router) NumShards() int {
	if r.mode == "shadow" {
		return len(r.shard)
	}
	return 1
}

// clientFor returns the target for key. Cluster mode returns the shared
// client (routing happens inside go-redis via CLUSTER SLOTS); shadow
// mode picks the per-shard client by CRC16.
func (r *router) clientFor(key string) redis.UniversalClient {
	if r.mode == "shadow" {
		return r.shard[r.sm.shard(key)]
	}
	return r.one
}

func (r *router) WaitReady(ctx context.Context, timeout time.Duration) error {
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
		if err := pingOne(r.one, r.mode); err != nil {
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

func (r *router) FlushAll(ctx context.Context) error {
	switch r.mode {
	case "standalone":
		return r.one.FlushDB(ctx).Err()
	case "cluster":
		// ClusterClient.FlushDB fans out to every master.
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
	return fmt.Errorf("unsupported mode %q", r.mode)
}

func (r *router) Close() {
	if r.one != nil {
		_ = r.one.Close()
	}
	for _, c := range r.shard {
		_ = c.Close()
	}
}

// UserToken must match loadgen's userToken(). See loadgen for the format
// rationale (MAID-shaped 32-hex-char string that round-trips through the
// identity-agent's canonicalizer unchanged).
func UserToken(i int) string { return fmt.Sprintf("%032x", i) }

func seedAudience(ctx context.Context, r *router, totalUsers, audsPerUser, totalAuds, workers, pipeSize int) {
	work := make(chan int, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seedWorker(ctx, r, work, pipeSize, func(u int, add func(key string, fields map[string]any, ttl time.Duration)) {
				token := UserToken(u)
				key := "audience:user:" + identityhash.Hash(token)
				fields := make(map[string]any, audsPerUser)
				for j := 0; j < audsPerUser; j++ {
					id := (u*audsPerUser + j) % totalAuds
					fields[fmt.Sprintf("aud-%05d", id)] = "1.0"
				}
				add(key, fields, 0)
			})
		}()
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	start := time.Now()
	for i := 0; i < totalUsers; i++ {
		select {
		case work <- i:
		case <-tick.C:
			log.Printf("  audience progress: %d/%d (%s)", i, totalUsers, time.Since(start))
			work <- i
		}
	}
	close(work)
	wg.Wait()
	log.Printf("  audience: seeded %d users in %s", totalUsers, time.Since(start))
}

func seedFCap(ctx context.Context, r *router, totalUsers int, userFraction float64, sellerURL string, totalPkgs, pkgsPerUser, workers, pipeSize int) {
	capped := int(float64(totalUsers) * userFraction)
	if capped == 0 {
		return
	}
	work := make(chan int, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seedWorker(ctx, r, work, pipeSize, func(u int, add func(key string, fields map[string]any, ttl time.Duration)) {
				token := UserToken(u)
				key := "fcap:" + identityhash.Hash(token)
				fields := make(map[string]any, pkgsPerUser)
				for j := 0; j < pkgsPerUser; j++ {
					id := (u*pkgsPerUser + j) % totalPkgs
					fields[sellerURL+":"+fmt.Sprintf("pkg-%05d", id)] = "1"
				}
				add(key, fields, 24*time.Hour)
			})
		}()
	}
	start := time.Now()
	for i := 0; i < capped; i++ {
		work <- i
	}
	close(work)
	wg.Wait()
	log.Printf("  fcap: seeded %d users in %s", capped, time.Since(start))
}

// seedWorker buckets writes by destination shard client (matters for
// shadow mode where each shard has its own pipeline), flushing when a
// bucket reaches pipeSize.
func seedWorker(ctx context.Context, r *router, work <-chan int, pipeSize int, build func(int, func(key string, fields map[string]any, ttl time.Duration))) {
	// Per-client pending pipeline. In standalone/cluster mode this map has
	// a single entry; in shadow mode there's one entry per shard.
	pending := make(map[redis.UniversalClient]redis.Pipeliner)
	counts := make(map[redis.UniversalClient]int)
	flush := func(c redis.UniversalClient) {
		p, ok := pending[c]
		if !ok || counts[c] == 0 {
			return
		}
		if _, err := p.Exec(ctx); err != nil {
			log.Fatalf("seed pipeline exec: %v", err)
		}
		delete(pending, c)
		delete(counts, c)
	}
	flushAll := func() {
		for c := range pending {
			flush(c)
		}
	}
	add := func(key string, fields map[string]any, ttl time.Duration) {
		c := r.clientFor(key)
		p, ok := pending[c]
		if !ok {
			p = c.Pipeline()
			pending[c] = p
		}
		p.HSet(ctx, key, fields)
		if ttl > 0 {
			p.Expire(ctx, key, ttl)
		}
		counts[c]++
		if counts[c] >= pipeSize {
			flush(c)
		}
	}
	for u := range work {
		build(u, add)
	}
	flushAll()
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
		log.Fatalf("%s=%q is not an integer: %v", name, v, err)
	}
	return n
}

func envFloat(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		log.Fatalf("%s=%q is not a float: %v", name, v, err)
	}
	return f
}
