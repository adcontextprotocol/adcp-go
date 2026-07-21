// seeder populates the audience and fcap Valkey backends with synthetic data
// shaped to what the identity-agent reads at request time.
//
// Key layouts (see targeting/audience and targeting/fcap):
//
//	audience:user:{sha256(user_token)[:16]}  HSET  field=audienceID  value=score
//	fcap:{sha256(user_token)[:16]}           HSET  field="{sellerURL}:{packageID}" value="1"
//
// Sized by env vars so the benchmark can shape memory pressure and match /
// miss ratios without a code change.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
	"github.com/redis/go-redis/v9"
)

func main() {
	audienceAddr := envStr("AUDIENCE_ADDR", "audience-valkey:6379")
	fcapAddr := envStr("FCAP_ADDR", "fcap-valkey:6379")

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

	audienceClient := redis.NewClient(&redis.Options{Addr: audienceAddr, PoolSize: workers})
	defer audienceClient.Close()
	fcapClient := redis.NewClient(&redis.Options{Addr: fcapAddr, PoolSize: workers})
	defer fcapClient.Close()

	if err := waitReady(ctx, audienceClient, "audience"); err != nil {
		log.Fatalf("%v", err)
	}
	if err := waitReady(ctx, fcapClient, "fcap"); err != nil {
		log.Fatalf("%v", err)
	}

	log.Printf("flushing audience + fcap stores")
	if err := audienceClient.FlushDB(ctx).Err(); err != nil {
		log.Fatalf("flush audience: %v", err)
	}
	if err := fcapClient.FlushDB(ctx).Err(); err != nil {
		log.Fatalf("flush fcap: %v", err)
	}

	if audiencesPerUser > 0 && totalAudiences > 0 {
		log.Printf("seeding audience: users=%d audiences/user=%d total_audiences=%d",
			totalUsers, audiencesPerUser, totalAudiences)
		seedAudience(ctx, audienceClient, totalUsers, audiencesPerUser, totalAudiences, workers, pipelineSize)
	} else {
		log.Printf("skipping audience seed (AUDIENCES_PER_USER=%d, TOTAL_AUDIENCES=%d)", audiencesPerUser, totalAudiences)
	}

	if packagesCappedPerUser > 0 && fcapUserFraction > 0 && totalPackages > 0 {
		log.Printf("seeding fcap: capped_users=%d packages_per_user=%d",
			int(float64(totalUsers)*fcapUserFraction), packagesCappedPerUser)
		seedFCap(ctx, fcapClient, totalUsers, fcapUserFraction, sellerAgentURL, totalPackages, packagesCappedPerUser, workers, pipelineSize)
	} else {
		log.Printf("skipping fcap seed (PACKAGES_CAPPED_PER_USER=%d, FCAP_USER_FRACTION=%g, TOTAL_PACKAGES=%d)",
			packagesCappedPerUser, fcapUserFraction, totalPackages)
	}

	log.Printf("seeder done")
}

func waitReady(ctx context.Context, c *redis.Client, name string) error {
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := c.Ping(ctx).Err(); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return fmt.Errorf("%s: not ready after 2m: %w", name, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// UserToken returns the deterministic token string for user index i. Kept in
// sync with loadgen so it hits the seeded users.
func UserToken(i int) string { return fmt.Sprintf("user-%08d", i) }

func seedAudience(ctx context.Context, c *redis.Client, totalUsers, audsPerUser, totalAuds, workers, pipeSize int) {
	work := make(chan int, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pipe := c.Pipeline()
			batched := 0
			flush := func() {
				if batched == 0 {
					return
				}
				if _, err := pipe.Exec(ctx); err != nil {
					log.Fatalf("audience pipe exec: %v", err)
				}
				batched = 0
			}
			for u := range work {
				token := UserToken(u)
				key := "audience:user:" + identityhash.Hash(token)
				fields := make(map[string]any, audsPerUser)
				for j := 0; j < audsPerUser; j++ {
					id := (u*audsPerUser + j) % totalAuds
					fields[fmt.Sprintf("aud-%05d", id)] = "1.0"
				}
				pipe.HSet(ctx, key, fields)
				batched++
				if batched >= pipeSize {
					flush()
				}
			}
			flush()
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

func seedFCap(ctx context.Context, c *redis.Client, totalUsers int, userFraction float64, sellerURL string, totalPkgs, pkgsPerUser, workers, pipeSize int) {
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
			pipe := c.Pipeline()
			batched := 0
			flush := func() {
				if batched == 0 {
					return
				}
				if _, err := pipe.Exec(ctx); err != nil {
					log.Fatalf("fcap pipe exec: %v", err)
				}
				batched = 0
			}
			for u := range work {
				token := UserToken(u)
				key := "fcap:" + identityhash.Hash(token)
				fields := make(map[string]any, pkgsPerUser)
				for j := 0; j < pkgsPerUser; j++ {
					id := (u*pkgsPerUser + j) % totalPkgs
					fields[sellerURL+":"+fmt.Sprintf("pkg-%05d", id)] = "1"
				}
				pipe.HSet(ctx, key, fields)
				pipe.Expire(ctx, key, 24*time.Hour)
				batched++
				if batched >= pipeSize {
					flush()
				}
			}
			flush()
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
