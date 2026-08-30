// writer generates realistic write load against the fcap and audience
// Valkey backends alongside identity-agent's read load. The identity-
// agent's read-only scaling numbers are an upper bound — in production
// the frequency-writer records cap markers when caps fire, and the
// audience-writer pushes membership updates. Running this alongside the
// loadgen produces mixed-workload numbers that reflect what an operator
// will actually see.
//
// Rate model (fcap):
//
//	Each fcap write mimics one cap-fire event: HSETEX(user_key,
//	fields=<PACKAGES_PER_WRITE>, ttl=serve_window) — one command per
//	call, batched over multiple fields, matching the frequency-writer's
//	SetFieldsBatch shape. Runs at WRITE_QPS_FCAP req/s.
//
// Rate model (audience):
//
//	Each audience write is a small HSET/HDEL flurry for one user across
//	a handful of audience_ids. Runs at WRITE_QPS_AUDIENCE req/s. Off by
//	default (0) since audience updates are typically batched off-peak.
package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/adcontextprotocol/adcp-go/bench/identity-perf/internal/router"
	"github.com/adcontextprotocol/adcp-go/targeting/identityhash"
	"github.com/redis/go-redis/v9"
)

func main() {
	totalUsers := envInt("TOTAL_USERS", 100_000)
	sellerAgentURL := envStr("SELLER_AGENT_URL", "https://seller.perf.local/agent")
	totalPackages := envInt("TOTAL_PACKAGES", 200)
	totalAudiences := envInt("TOTAL_AUDIENCES", 500)

	writeQpsFcap := envInt("WRITE_QPS_FCAP", 0)
	writeQpsAudience := envInt("WRITE_QPS_AUDIENCE", 0)
	packagesPerWrite := envInt("PACKAGES_PER_WRITE", 2)
	audiencesPerWrite := envInt("AUDIENCES_PER_WRITE", 1)
	serveWindow := envDuration("SERVE_WINDOW", 60*time.Second)
	duration := envDuration("DURATION", 60*time.Second)
	concurrency := envInt("CONCURRENCY", 32)

	if writeQpsFcap <= 0 && writeQpsAudience <= 0 {
		log.Printf("writer: nothing to do (WRITE_QPS_FCAP=0, WRITE_QPS_AUDIENCE=0)")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	var wg sync.WaitGroup
	if writeQpsFcap > 0 {
		fcapR, err := router.New(context.Background(), "FCAP")
		if err != nil {
			log.Fatalf("fcap router: %v", err)
		}
		defer fcapR.Close()
		log.Printf("fcap writer: %d qps, %d packages/write, ttl=%s, topology=%s shards=%d",
			writeQpsFcap, packagesPerWrite, serveWindow, fcapR.Mode, fcapR.NumShards())
		wg.Go(func() {
			runFcapWriter(ctx, fcapR, writeQpsFcap, concurrency,
				totalUsers, totalPackages, packagesPerWrite, sellerAgentURL, serveWindow)
		})
	}
	if writeQpsAudience > 0 {
		audR, err := router.New(context.Background(), "AUDIENCE")
		if err != nil {
			log.Fatalf("audience router: %v", err)
		}
		defer audR.Close()
		log.Printf("audience writer: %d qps, %d audiences/write, topology=%s shards=%d",
			writeQpsAudience, audiencesPerWrite, audR.Mode, audR.NumShards())
		wg.Go(func() {
			runAudienceWriter(ctx, audR, writeQpsAudience, concurrency,
				totalUsers, totalAudiences, audiencesPerWrite)
		})
	}
	wg.Wait()
}

func runFcapWriter(ctx context.Context, r *router.Router, qps, concurrency, totalUsers, totalPkgs, pkgsPerWrite int, sellerURL string, ttl time.Duration) {
	var (
		submitted atomic.Int64
		ok        atomic.Int64
		errored   atomic.Int64
	)
	tickets := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
			for range tickets {
				u := rng.IntN(totalUsers)
				key := "fcap:" + identityhash.Hash(fmt.Sprintf("%032x", u))
				// HSETEX with N fields, matching redisstore.Store.SetFields.
				args := make([]string, 0, pkgsPerWrite*2)
				for range pkgsPerWrite {
					pkgID := rng.IntN(totalPkgs)
					field := sellerURL + ":" + fmt.Sprintf("pkg-%05d", pkgID)
					args = append(args, field, "1")
				}
				expireAt := time.Now().Add(ttl).UnixMilli()
				opts := &redis.HSetEXOptions{
					ExpirationType: redis.HSetEXExpirationPXAT,
					ExpirationVal:  expireAt,
				}
				if err := r.ClientFor(key).HSetEXWithArgs(ctx, key, opts, args...).Err(); err != nil {
					if ctx.Err() == nil {
						errored.Add(1)
					}
					continue
				}
				ok.Add(1)
			}
		}(uint64(i) + 1)
	}
	pace(ctx, qps, func() {
		submitted.Add(1)
		select {
		case tickets <- struct{}{}:
		default:
			// Consumer can't keep up; drop this tick and yield —
			// spinning on the drop path at saturation pins a core
			// and inflates SUT-observed latency if writer and SUT
			// share cores.
			runtime.Gosched()
		}
	})
	close(tickets)
	wg.Wait()
	log.Printf("fcap writer: submitted=%d ok=%d errored=%d", submitted.Load(), ok.Load(), errored.Load())
}

func runAudienceWriter(ctx context.Context, r *router.Router, qps, concurrency, totalUsers, totalAuds, audsPerWrite int) {
	var (
		submitted atomic.Int64
		ok        atomic.Int64
		errored   atomic.Int64
	)
	tickets := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range concurrency {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, seed^0xdeadbeefcafebabe))
			for range tickets {
				u := rng.IntN(totalUsers)
				key := "audience:user:" + identityhash.Hash(fmt.Sprintf("%032x", u))
				fields := make(map[string]any, audsPerWrite)
				for range audsPerWrite {
					id := rng.IntN(totalAuds)
					fields[fmt.Sprintf("aud-%05d", id)] = "1.0"
				}
				if err := r.ClientFor(key).HSet(ctx, key, fields).Err(); err != nil {
					if ctx.Err() == nil {
						errored.Add(1)
					}
					continue
				}
				ok.Add(1)
			}
		}(uint64(i) + 1)
	}
	pace(ctx, qps, func() {
		submitted.Add(1)
		select {
		case tickets <- struct{}{}:
		default:
			runtime.Gosched()
		}
	})
	close(tickets)
	wg.Wait()
	log.Printf("audience writer: submitted=%d ok=%d errored=%d", submitted.Load(), ok.Load(), errored.Load())
}

// pace fires cb at target qps, using the same deadline-based pacer the
// loadgen uses so real throughput matches the target.
func pace(ctx context.Context, qps int, cb func()) {
	if qps <= 0 {
		return
	}
	interval := time.Second / time.Duration(qps)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	start := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for i := int64(0); ; i++ {
		next := start.Add(time.Duration(i) * interval)
		delay := time.Until(next)
		if delay > 0 {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		cb()
	}
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

func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("%s=%q is not a duration: %v", name, v, err)
	}
	return d
}
