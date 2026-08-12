// Command seed populates the stack's Valkey with the state the two agents
// read at request time.
//
// Every write goes through the same store package the agent reads through —
// mediabuystore, pkgconfigstore, topicstore, signalstore, suppressionstore,
// audience, fcap — so no key layout is ever hand-written here. Note the limit
// of that: those packages are separate modules resolved from the proxy at their
// published version in every image, so an unreleased key-layout change is
// absent from both the seeder and the agent rather than caught by either. See
// e2e/stack/README.md.
//
// Three logical databases keep the read paths apart:
//
//	db 0  context-agent   media buys, package configs, topics, signals, suppressions
//	db 1  identity-agent  audience membership
//	db 2  identity-agent  frequency-cap markers
//
// Each database is flushed before seeding. A re-run is therefore idempotent,
// and state left behind by an earlier scenario cannot leak into the
// assertions.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/adcontextprotocol/adcp-go/e2e/stack/internal/fixture"
	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/audience"
	"github.com/adcontextprotocol/adcp-go/targeting/fcap"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/signalstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/urlcanon"
)

const valkeyReadyTimeout = 2 * time.Minute

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The context engine canonicalizes seller_agent_url on read, so the
	// seller keys have to be written in canonical form or the active-set
	// lookup misses everything the seeder wrote.
	seller, err := urlcanon.Canonicalize(fixture.SellerAgentURL)
	if err != nil {
		log.Fatalf("seed: canonicalize seller %q: %v", fixture.SellerAgentURL, err)
	}

	contextClient := dial(fixture.ContextValkeyDB)
	defer closeClient(contextClient)
	audienceClient := dial(fixture.AudienceValkeyDB)
	defer closeClient(audienceClient)
	fcapClient := dial(fixture.FCapValkeyDB)
	defer closeClient(fcapClient)

	for _, c := range []*redis.Client{contextClient, audienceClient, fcapClient} {
		if err := waitReady(ctx, c); err != nil {
			log.Fatalf("seed: %v", err)
		}
		if err := c.FlushDB(ctx).Err(); err != nil {
			log.Fatalf("seed: flushdb: %v", err)
		}
	}

	if err := seedContext(ctx, contextClient, seller); err != nil {
		log.Fatalf("seed: context: %v", err)
	}
	if err := seedAudience(ctx, audienceClient); err != nil {
		log.Fatalf("seed: audience: %v", err)
	}
	if err := seedFCap(ctx, fcapClient, seller); err != nil {
		log.Fatalf("seed: fcap: %v", err)
	}

	log.Printf("seed: done")
}

// seedContext writes everything the context-agent reads: one media buy per
// package under the shared seller, the per-package context config that
// carries each package's gate, the topic sets the topic gate intersects,
// the signal key the signal gate matches, and the property-level
// suppression marker that proves the kill switch drops offers.
func seedContext(ctx context.Context, client *redis.Client, seller string) error {
	store := redisstore.New(client)

	buys, err := mediabuystore.NewService(store)
	if err != nil {
		return fmt.Errorf("mediabuystore service: %w", err)
	}
	configs, err := pkgconfigstore.NewService(store)
	if err != nil {
		return fmt.Errorf("pkgconfigstore service: %w", err)
	}
	topics, err := topicstore.NewWriter(store)
	if err != nil {
		return fmt.Errorf("topicstore writer: %w", err)
	}
	suppressions, err := suppressionstore.NewService(store)
	if err != nil {
		return fmt.Errorf("suppressionstore service: %w", err)
	}

	// One buy per package. PropertyIDs, PlacementIDs and Countries are all
	// left empty, which the engine reads as "every one of the seller's" —
	// so property and placement scoping is expressed on the package config
	// (PropertyRIDs) where the assertions can attribute it precisely.
	for _, pkgID := range fixture.ContextPackages {
		buyID := fixture.MediaBuyID(pkgID)
		if err := buys.Put(ctx, mediabuystore.MediaBuy{
			MediaBuyID:     buyID,
			SellerAgentURL: seller,
			StartDate:      "2020-01-01",
			EndDate:        "2099-12-31",
			Packages: []mediabuystore.MediaBuyPackage{{
				PackageID:  pkgID,
				MediaBuyID: buyID,
			}},
		}); err != nil {
			return fmt.Errorf("media buy %s: %w", buyID, err)
		}
	}

	for _, cfg := range contextPackageConfigs() {
		if err := configs.Put(ctx, cfg); err != nil {
			return fmt.Errorf("package config %s: %w", cfg.PackageID, err)
		}
	}

	// Topic gate: the package and the matched artifact share TopicNews, so
	// their intersection is non-empty. The unmatched artifact carries only
	// TopicFinance, so the same package fails closed for it.
	if err := topics.SetPackageTopics(ctx, fixture.AcceptedTaxonomy,
		fixture.PackageContextTopic, []string{fixture.TopicNews}); err != nil {
		return fmt.Errorf("package topics: %w", err)
	}
	if err := topics.SetArtifactTopics(ctx, fixture.AcceptedTaxonomy,
		fixture.ArtifactMatched, []string{fixture.TopicNews}); err != nil {
		return fmt.Errorf("matched artifact topics: %w", err)
	}
	if err := topics.SetArtifactTopics(ctx, fixture.AcceptedTaxonomy,
		fixture.ArtifactUnmatched, []string{fixture.TopicFinance}); err != nil {
		return fmt.Errorf("unmatched artifact topics: %w", err)
	}

	// Signal gate: one key for the matched artifact only. The unmatched
	// artifact has no key, so the any_of cfg finds nothing. The key's value
	// segment is the hashed form the engine derives from a `url` artifact
	// ref — see fixture.SignalValue.
	signalKey := signalstore.Key(fixture.SignalOwnerID, fixture.SignalKeyTypes,
		[]string{fixture.SignalValue(fixture.ArtifactMatched)})
	if signalKey == "" {
		return fmt.Errorf("signalstore.Key returned empty for %q", fixture.ArtifactMatched)
	}
	if err := store.Set(ctx, signalKey, fixture.SignalID, 0); err != nil {
		return fmt.Errorf("signal key %s: %w", signalKey, err)
	}

	// Property kill switch on the shuttered property. The agent re-scans
	// suppress:{provider_id}:* on SUPPRESSION_REFRESH_INTERVAL and also
	// loads the set at startup, so seeding before the agent boots makes the
	// marker visible on its first request.
	if err := suppressions.SuppressProperty(ctx, fixture.ContextProviderID,
		fixture.ShutteredProperty().PropertyRID, fixture.SuppressionTTLHours*time.Hour); err != nil {
		return fmt.Errorf("suppress property: %w", err)
	}

	log.Printf("seed: context — %d packages, 2 artifacts, 1 signal key, 1 suppressed property",
		len(fixture.ContextPackages))
	return nil
}

// contextPackageConfigs returns one config per context package, each
// carrying exactly one gate so a failing assertion names the gate that
// broke.
func contextPackageConfigs() []*targeting.PackageContextConfig {
	return []*targeting.PackageContextConfig{
		{
			PackageID: fixture.PackageContextOpen,
			Summary:   fixture.OfferSummary(fixture.PackageContextOpen),
		},
		{
			PackageID:    fixture.PackageContextTopic,
			Summary:      fixture.OfferSummary(fixture.PackageContextTopic),
			TopicTargets: true,
		},
		{
			PackageID: fixture.PackageContextSignal,
			Summary:   fixture.OfferSummary(fixture.PackageContextSignal),
			ContextSignals: &signalstore.Profile{
				AnyOf: []signalstore.Cfg{{
					SignalOwnerID: fixture.SignalOwnerID,
					KeyTypes:      fixture.SignalKeyTypes,
					SignalID:      fixture.SignalID,
				}},
			},
		},
		{
			PackageID:    fixture.PackageContextVideoOnly,
			Summary:      fixture.OfferSummary(fixture.PackageContextVideoOnly),
			PropertyRIDs: []string{fixture.VideoProperty().PropertyRID},
		},
	}
}

// seedAudience makes the segmented user a member of the stack's one
// segment. The anonymous user is deliberately left out so the audience gate
// has both a pass and a fail case.
func seedAudience(ctx context.Context, client *redis.Client) error {
	svc := audience.New(redisstore.New(client))
	if err := svc.Upsert(ctx, audience.AudienceUpsert{
		AudienceID: fixture.AudienceSegment,
		Add: []audience.Member{{
			UserToken: fixture.UserSegmented(),
			Score:     fixture.AudienceScore,
		}},
	}); err != nil {
		return fmt.Errorf("upsert %s: %w", fixture.AudienceSegment, err)
	}
	log.Printf("seed: audience — 1 member in %s", fixture.AudienceSegment)
	return nil
}

// seedFCap records a frequency-cap marker for the segmented user on the
// capped package only. The anonymous user gets none, so the same package
// resolves differently per user and the assertion can only pass if the
// marker was actually read.
func seedFCap(ctx context.Context, client *redis.Client, seller string) error {
	svc := fcap.New(redisstore.New(client))
	expireAt := time.Now().Add(fixture.FCapTTLHours * time.Hour)
	err := svc.RecordCap(ctx, fixture.UserSegmented(), []fcap.Field{{
		SellerAgentURL: seller,
		PackageID:      fixture.PackageIdentityCapped,
	}}, expireAt)
	if err != nil {
		return fmt.Errorf("record cap: %w", err)
	}
	log.Printf("seed: fcap — 1 marker on %s", fixture.PackageIdentityCapped)
	return nil
}

func dial(db int) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: fixture.ValkeyAddr, DB: db})
}

func closeClient(c *redis.Client) { _ = c.Close() }

func waitReady(ctx context.Context, c *redis.Client) error {
	deadline := time.Now().Add(valkeyReadyTimeout)
	var last error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last = c.Ping(ctx).Err()
		if last == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("valkey %s not ready after %s: %w", fixture.ValkeyAddr, valkeyReadyTimeout, last)
}
