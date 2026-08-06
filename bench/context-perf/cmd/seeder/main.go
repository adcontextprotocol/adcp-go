// seeder populates the single Valkey backing the context-agent with
// the state a /context request needs to evaluate to a non-empty offer
// list.
//
// What is written (all through the store packages so key formats stay
// in lockstep with what context-agent reads):
//
//   - mediabuystore: one MediaBuy per synthetic package under a
//     deterministic seller_agent_url. Each buy carries a single
//     MediaBuyPackage referencing the same package id. PropertyIDs is
//     empty (= all seller properties), Countries is empty (= all
//     countries), PlacementIDs is empty (= all placements) so any
//     request the loadgen emits matches every active buy.
//   - pkgconfigstore: one PackageContextConfig per package. Carries a
//     minimal Summary so the response can be inspected. When
//     TOPIC_TARGETS_ENABLED=true, TopicTargets is set to true and the
//     engine consults topicstore per-artifact. When SIGNALS_ENABLED,
//     ContextSignals is populated with an any_of Cfg gated on the
//     seeded signal.
//   - topicstore: when TOPIC_TARGETS_ENABLED, seeds per-package topic
//     sets AND per-artifact topic sets under
//     ACCEPTED_TAXONOMY_SOURCE:ACCEPTED_TAXONOMY_ID so intersection is
//     non-empty on every request the loadgen picks.
//   - signalstore: when SIGNALS_ENABLED, seeds signal:{owner}:{key_types}:{values}
//     keys carrying the seeded signal_id in their CSV payload, so an
//     any_of Cfg with the same shape matches at request time.
//   - suppressionstore: no baseline suppressions. The engine's
//     suppression snapshot loads an empty list; every request passes
//     the kill-switch gate.
//
// Environment (with defaults):
//
//	VALKEY_ADDR                    "valkey:6379"
//	SELLER_AGENT_URL               <corpus.SellerAgentURL>
//	TOTAL_PACKAGES                 200
//	TOTAL_ARTIFACTS                1000  (URL pool loadgen draws from
//	                                      AND the signal-key count when
//	                                      SIGNALS_ENABLED — one knob
//	                                      keeps the pools in lockstep)
//	TOPIC_TARGETS_ENABLED          false
//	TOTAL_TOPICS                   500
//	TOPICS_PER_PACKAGE             5
//	TOPICS_PER_ARTIFACT            10
//	SIGNALS_ENABLED                false
//
// The Valkey is always FLUSHDB'd before seeding to guarantee no
// cross-scenario key contamination. The signal key_type is fixed to
// url_hash — the only type the loadgen emits — so overrides can't
// silently break the seed↔read pairing.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/bench/context-perf/internal/corpus"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/urlcanon"
)

func main() {
	valkeyAddr := envStr("VALKEY_ADDR", "valkey:6379")
	rawSellerURL := envStr("SELLER_AGENT_URL", corpus.SellerAgentURL)
	// The engine canonicalizes seller_agent_url on read
	// (targeting/engine.go). Canonicalize here so overrides that aren't
	// already canonical (mixed case, default port, trailing slash) still
	// write keys the engine will look up.
	sellerURL, err := urlcanon.Canonicalize(rawSellerURL)
	if err != nil {
		log.Fatalf("canonicalize SELLER_AGENT_URL=%q: %v", rawSellerURL, err)
	}
	totalPackages := envInt("TOTAL_PACKAGES", 200)
	totalArtifacts := envInt("TOTAL_ARTIFACTS", 1000)
	topicsEnabled := envBool("TOPIC_TARGETS_ENABLED", false)
	totalTopics := envInt("TOTAL_TOPICS", 500)
	topicsPerPackage := envInt("TOPICS_PER_PACKAGE", 5)
	topicsPerArtifact := envInt("TOPICS_PER_ARTIFACT", 10)
	signalsEnabled := envBool("SIGNALS_ENABLED", false)
	// key_type and signal-value count are intentionally NOT env-tunable:
	// - loadgen only emits ArtifactRefType=url_hash, so any other
	//   SIGNAL_KEY_TYPE would silently seed keys the engine never reads.
	// - the signal-key count is coupled to the artifact pool the loadgen
	//   draws from; unifying on TOTAL_ARTIFACTS makes decoupling
	//   impossible.
	signalKeyType := signalstore.KeyTypeURLHash

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	client := redis.NewClient(&redis.Options{Addr: valkeyAddr})
	defer func() { _ = client.Close() }()
	if err := waitReady(ctx, client, 2*time.Minute); err != nil {
		log.Fatalf("valkey %s: %v", valkeyAddr, err)
	}
	store := redisstore.New(client)

	// Always flush before seeding — cross-scenario key contamination
	// (topics from packages-topics leaking into packages-signals, etc.)
	// silently invalidates comparisons and there is no runtime signal
	// that it happened. Non-negotiable.
	log.Printf("flushing valkey at %s", valkeyAddr)
	if err := client.FlushDB(ctx).Err(); err != nil {
		log.Fatalf("flushdb: %v", err)
	}

	mbSvc, err := mediabuystore.NewService(store)
	if err != nil {
		log.Fatalf("mediabuystore service: %v", err)
	}
	pkgSvc, err := pkgconfigstore.NewService(store)
	if err != nil {
		log.Fatalf("pkgconfigstore service: %v", err)
	}
	topicWriter, err := topicstore.NewWriter(store)
	if err != nil {
		log.Fatalf("topicstore writer: %v", err)
	}

	// media buys + package configs
	log.Printf("seeding %d packages under seller=%s (canonicalized from %q) topics=%v signals=%v",
		totalPackages, sellerURL, rawSellerURL, topicsEnabled, signalsEnabled)
	start := time.Now()
	for i := 0; i < totalPackages; i++ {
		pkgID := corpus.PackageID(i)
		mbID := corpus.MediaBuyID(i)
		if err := mbSvc.Put(ctx, mediabuystore.MediaBuy{
			MediaBuyID:     mbID,
			SellerAgentURL: sellerURL,
			StartDate:      "2020-01-01",
			EndDate:        "2099-12-31",
			Packages: []mediabuystore.MediaBuyPackage{{
				PackageID:  pkgID,
				MediaBuyID: mbID,
			}},
		}); err != nil {
			log.Fatalf("mediabuy put %s: %v", mbID, err)
		}

		cfg := &targeting.PackageContextConfig{
			PackageID: pkgID,
			Summary:   fmt.Sprintf("perf offer %d", i),
		}
		if topicsEnabled {
			cfg.TopicTargets = true
		}
		if signalsEnabled {
			cfg.ContextSignals = &signalstore.Profile{
				AnyOf: []signalstore.Cfg{{
					SignalOwnerID: corpus.SignalOwnerID,
					KeyTypes:      []signalstore.KeyType{signalKeyType},
					SignalID:      corpus.SignalID,
				}},
			}
		}
		if err := pkgSvc.Put(ctx, cfg); err != nil {
			log.Fatalf("pkgconfig put %s: %v", pkgID, err)
		}
	}
	log.Printf("  packages seeded in %s", time.Since(start))

	if topicsEnabled {
		if topicsPerPackage < 1 || topicsPerArtifact < 1 {
			log.Fatalf("TOPICS_PER_PACKAGE and TOPICS_PER_ARTIFACT must be >= 1 (need the shared always-on topic)")
		}
		needExtras := topicsPerPackage > 1 || topicsPerArtifact > 1
		if needExtras && totalTopics < 2 {
			log.Fatalf("TOTAL_TOPICS must be >= 2 when TOPICS_PER_PACKAGE or TOPICS_PER_ARTIFACT is > 1")
		}
		if totalTopics < 1 {
			log.Fatalf("TOTAL_TOPICS must be positive when TOPIC_TARGETS_ENABLED=true")
		}
		log.Printf("seeding topics: %d packages × %d topics + %d artifacts × %d topics",
			totalPackages, topicsPerPackage, totalArtifacts, topicsPerArtifact)
		start = time.Now()
		// Shared always-on topic (index 0) attached to every package
		// AND every artifact — guarantees every (artifact, package)
		// pair intersects on at least this one topic so the engine's
		// topic gate warms the topicstore MGet + intersection path
		// instead of rejecting every candidate as an empty-intersection
		// fail-close.
		//
		// The additional per-package / per-artifact topics (indices
		// >0) exercise the wider taxonomy without gating the outcome
		// on whether their windows happen to overlap.
		alwaysOn := corpus.TopicID(0)
		for i := 0; i < totalPackages; i++ {
			pkgID := corpus.PackageID(i)
			topics := make([]string, 0, topicsPerPackage)
			topics = append(topics, alwaysOn)
			for j := 1; j < topicsPerPackage; j++ {
				topics = append(topics, corpus.TopicID(1+(i*topicsPerPackage+j)%(totalTopics-1)))
			}
			if err := topicWriter.SetPackageTopics(ctx, corpus.AcceptedTaxonomy, pkgID, topics); err != nil {
				log.Fatalf("package topics %s: %v", pkgID, err)
			}
		}
		for i := 0; i < totalArtifacts; i++ {
			ref := corpus.ArtifactURL(i)
			topics := make([]string, 0, topicsPerArtifact)
			topics = append(topics, alwaysOn)
			for j := 1; j < topicsPerArtifact; j++ {
				topics = append(topics, corpus.TopicID(1+(i*topicsPerArtifact+j)%(totalTopics-1)))
			}
			if err := topicWriter.SetArtifactTopics(ctx, corpus.AcceptedTaxonomy, ref, topics); err != nil {
				log.Fatalf("artifact topics %s: %v", ref, err)
			}
		}
		log.Printf("  topics seeded in %s", time.Since(start))
	}

	if signalsEnabled {
		if totalArtifacts <= 0 {
			log.Fatalf("TOTAL_ARTIFACTS must be positive when SIGNALS_ENABLED=true")
		}
		log.Printf("seeding %d signal keys under owner=%s key_type=%s",
			totalArtifacts, corpus.SignalOwnerID, signalKeyType)
		start = time.Now()
		pipe := client.Pipeline()
		pending := 0
		const pipelineBatch = 1000
		flushPipe := func() {
			if pending == 0 {
				return
			}
			if _, err := pipe.Exec(ctx); err != nil {
				log.Fatalf("signal pipeline exec: %v", err)
			}
			pipe = client.Pipeline()
			pending = 0
		}
		// The loadgen draws artifact URLs from corpus.ArtifactURL(i)
		// with i in [0, TOTAL_ARTIFACTS). Seed one signal key per URL
		// in the exact same range so every request-time MGet hits a
		// seeded key. Unifying the count on TOTAL_ARTIFACTS makes it
		// impossible for an operator to decouple the two pools and
		// silently measure cold misses.
		for i := 0; i < totalArtifacts; i++ {
			val := corpus.ArtifactURL(i)
			key := signalstore.Key(corpus.SignalOwnerID,
				[]signalstore.KeyType{signalKeyType},
				[]string{val})
			if key == "" {
				log.Fatalf("signalstore.Key returned empty for value %q", val)
			}
			pipe.Set(ctx, key, corpus.SignalID, 0)
			pending++
			if pending >= pipelineBatch {
				flushPipe()
			}
		}
		flushPipe()
		log.Printf("  signals seeded in %s", time.Since(start))
	}

	log.Printf("seeder done")
}

func waitReady(ctx context.Context, c *redis.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := c.Ping(ctx).Err(); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("valkey not ready after %s: %w", timeout, last)
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

func envBool(name string, def bool) bool {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("%s=%q is not a bool: %v", name, v, err)
	}
	return b
}
