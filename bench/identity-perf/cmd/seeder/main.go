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
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/bench/identity-perf/internal/router"
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

	audienceRouter, err := router.New(ctx, "AUDIENCE")
	if err != nil {
		log.Fatalf("audience: %v", err)
	}
	defer audienceRouter.Close()
	fcapRouter, err := router.New(ctx, "FCAP")
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
			totalUsers, audiencesPerUser, totalAudiences, audienceRouter.Mode, audienceRouter.NumShards())
		seedAudience(ctx, audienceRouter, totalUsers, audiencesPerUser, totalAudiences, workers, pipelineSize)
	} else {
		log.Printf("skipping audience seed (AUDIENCES_PER_USER=%d, TOTAL_AUDIENCES=%d)",
			audiencesPerUser, totalAudiences)
	}

	if packagesCappedPerUser > 0 && fcapUserFraction > 0 && totalPackages > 0 {
		log.Printf("seeding fcap: capped_users=%d packages_per_user=%d topology=%s shards=%d",
			int(float64(totalUsers)*fcapUserFraction), packagesCappedPerUser, fcapRouter.Mode, fcapRouter.NumShards())
		seedFCap(ctx, fcapRouter, totalUsers, fcapUserFraction, sellerAgentURL, totalPackages, packagesCappedPerUser, workers, pipelineSize)
	} else {
		log.Printf("skipping fcap seed (PACKAGES_CAPPED_PER_USER=%d, FCAP_USER_FRACTION=%g, TOTAL_PACKAGES=%d)",
			packagesCappedPerUser, fcapUserFraction, totalPackages)
	}

	log.Printf("seeder done")
}

// UserToken must match loadgen's userToken(). See loadgen for the format
// rationale (MAID-shaped 32-hex-char string that round-trips through the
// identity-agent's canonicalizer unchanged).
func UserToken(i int) string { return fmt.Sprintf("%032x", i) }

func seedAudience(ctx context.Context, r *router.Router, totalUsers, audsPerUser, totalAuds, workers, pipeSize int) {
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

func seedFCap(ctx context.Context, r *router.Router, totalUsers int, userFraction float64, sellerURL string, totalPkgs, pkgsPerUser, workers, pipeSize int) {
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
func seedWorker(ctx context.Context, r *router.Router, work <-chan int, pipeSize int, build func(int, func(key string, fields map[string]any, ttl time.Duration))) {
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
		c := r.ClientFor(key)
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
