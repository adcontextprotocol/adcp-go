//go:build integration

package contextagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/redisstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestSmoke_ContextAgent_EndToEnd is the Valkey-smoke gate from PR #360's
// test plan. It composes the full agent storage chain — Valkey backend,
// per-domain Service writers, buildBundle, NewServer — against a real
// Valkey container, seeds an active media buy + package config, and
// drives a context-match request through the HTTP handler. Asserts the
// response carries the expected offer and that the engine actually
// consulted the storage chain end-to-end.
//
// Skipped automatically when Docker is unavailable. Run with:
//
//	go test -tags=integration -run TestSmoke_ContextAgent ./targeting/contextagent/...
func TestSmoke_ContextAgent_EndToEnd(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	addr := startValkeyForSmoke(t)
	cfg := smokeConfig(addr)

	// Seed the populated state operators would build via their writer
	// pipeline. Same Service surfaces a real writer would use.
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	store := redisstore.New(client)
	seedSmokeData(t, ctx, store, cfg)

	// buildBundle exercises the production wiring: Valkey connect,
	// suppression initial load (fail-closed), per-domain readers,
	// engine construction, keystore (no-op because AllowUnsigned).
	metricsProvider, err := BuildMetrics(cfg.Metrics)
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsProvider.Shutdown(ctx) })
	bundle, err := buildBundle(ctx, cfg, runOptions{}, metricsProvider.Recorder, logger)
	require.NoError(t, err)
	t.Cleanup(func() { teardownBundle(bundle) })

	// Wire the same handler chain Run wires, then expose it via
	// httptest so the test owns the listener lifecycle (no signal
	// handling, no shutdown race).
	srv := NewServer(ServerConfig{
		Port:              0, // unused — httptest picks
		ContextHandler:    NewHandler(handlerCfgFor(cfg, bundle, metricsProvider.Recorder, logger)),
		KeyStore:          bundle.keystore,
		OwnEndpointURL:    cfg.TMP.OwnEndpointURL,
		RequireSig:        !cfg.TMP.AllowUnsigned,
		Registry:          metricsProvider.Registry,
		IsRunning:         func() bool { return true },
		Version:           "smoke",
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
		RequestBodyLimit:  int64(cfg.RequestBodyLimitBytes),
		AdminPort:         0,
		StrictContentType: cfg.StrictContentType,
		Recorder:          metricsProvider.Recorder,
		Logger:            logger,
	})
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)

	t.Run("matches_active_package", func(t *testing.T) {
		body := mustEncodeRequest(t, &tmproto.ContextMatchRequest{
			Type:            tmproto.TypeContextMatchRequest,
			ProtocolVersion: "1.0",
			RequestID:       "smoke-req-1",
			PropertyID:      "pub-1",
			PropertyRID:     "rid-1",
			PropertyType:    "site",
			PlacementID:     "slot-A",
		})
		resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode, "smoke request must succeed end-to-end")

		var parsed tmproto.ContextMatchResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&parsed))
		assert.Equal(t, tmproto.TypeContextMatchResponse, parsed.Type)
		assert.Equal(t, "smoke-req-1", parsed.RequestID)
		require.Len(t, parsed.Offers, 1, "seeded package must surface as one offer")
		assert.Equal(t, "pkg-smoke", parsed.Offers[0].PackageID)
	})

	t.Run("explicit_package_ids_intersect_with_active_set", func(t *testing.T) {
		// Naming an unknown package_id must silent-drop (intersection
		// rule). Naming the active one must still surface it.
		body := mustEncodeRequest(t, &tmproto.ContextMatchRequest{
			Type:            tmproto.TypeContextMatchRequest,
			ProtocolVersion: "1.0",
			RequestID:       "smoke-req-2",
			PropertyID:      "pub-1",
			PropertyRID:     "rid-1",
			PropertyType:    "site",
			PlacementID:     "slot-A",
			PackageIDs:      []string{"pkg-not-registered", "pkg-smoke"},
		})
		resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var parsed tmproto.ContextMatchResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&parsed))
		require.Len(t, parsed.Offers, 1)
		assert.Equal(t, "pkg-smoke", parsed.Offers[0].PackageID,
			"unknown package_id must be silently dropped; active one must surface")
	})

	t.Run("property_suppression_blocks_traffic", func(t *testing.T) {
		// Write a property suppression through the Service surface,
		// force the snapshot to pick it up (the live agent would
		// refresh every SUPPRESSION_REFRESH_INTERVAL — too slow for
		// a test, so re-load directly), then verify the request
		// short-circuits with zero offers.
		suppressSvc, err := suppressionstore.NewService(store)
		require.NoError(t, err)
		require.NoError(t, suppressSvc.SuppressProperty(ctx, cfg.ProviderID, "rid-1", time.Hour))
		require.NoError(t, bundle.suppressionSnap.Load(ctx))

		body := mustEncodeRequest(t, &tmproto.ContextMatchRequest{
			Type:            tmproto.TypeContextMatchRequest,
			ProtocolVersion: "1.0",
			RequestID:       "smoke-req-3",
			PropertyID:      "pub-1",
			PropertyRID:     "rid-1",
			PropertyType:    "site",
			PlacementID:     "slot-A",
		})
		resp, err := http.Post(ts.URL+"/context", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var parsed tmproto.ContextMatchResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&parsed))
		assert.Empty(t, parsed.Offers, "suppressed property must short-circuit at the top of Evaluate")
	})
}

// smokeConfig returns a Config wired against the supplied Valkey
// address. AllowUnsigned bypasses TMP signing — the smoke is about the
// storage→engine→handler chain; signing has its own coverage.
func smokeConfig(valkeyAddr string) Config {
	return Config{
		HTTPPort:                   0,
		RequestTimeout:             500 * time.Millisecond,
		HTTPReadHeaderTimeout:      500 * time.Millisecond,
		HTTPReadTimeout:            time.Second,
		HTTPWriteTimeout:           2 * time.Second,
		HTTPIdleTimeout:            10 * time.Second,
		ShutdownGrace:              0,
		ShutdownTimeout:            5 * time.Second,
		RequestBodyLimitBytes:      256 * 1024,
		MaxHeaderBytes:             8 * 1024,
		MaxOpenConnections:         16,
		ResponseTTL:                60 * time.Second,
		StrictContentType:          true,
		AdminPort:                  0,
		SupportedADCPMajorVersions: []int{3},
		LogLevel:                   "info",
		ProviderID:                 "provider-smoke",
		SellerAgentURL:             "https://seller.example/agent",
		PropertyRIDs:               []string{"rid-1"},
		SuppressionRefreshInterval: time.Minute,
		TMP: TMPConfig{
			AllowUnsigned: true,
		},
		Valkey: ValkeyBlock{
			Enabled:        true,
			ShardsSupplied: true,
			Mode:           "standalone",
			Shards:         map[string]string{"0": valkeyAddr},
			DialTimeout:    2 * time.Second,
			ReadTimeout:    500 * time.Millisecond,
			WriteTimeout:   500 * time.Millisecond,
		},
		Cache: CacheConfig{Enabled: false},
		// Metrics off so the test doesn't need a Prometheus registry
		// scrape lifecycle; recorder is the noop one from BuildMetrics.
		Metrics: MetricsConfig{Enabled: false, Namespace: "context_agent"},
	}
}

func handlerCfgFor(cfg Config, bundle *bundle, recorder Recorder, logger *slog.Logger) HandlerConfig {
	return HandlerConfig{
		Engine:                     bundle.engine,
		RequestTimeout:             cfg.RequestTimeout,
		RequestBodyLimit:           int64(cfg.RequestBodyLimitBytes),
		ResponseTTL:                cfg.ResponseTTL,
		SupportedADCPMajorVersions: cfg.SupportedADCPMajorVersions,
		Recorder:                   recorder,
		Logger:                     logger,
	}
}

// seedSmokeData populates the minimum Valkey state needed for one
// active package to surface on a /context request: one media buy
// pointing at the seller_agent_url, one package config naming the
// expected offer.
func seedSmokeData(t *testing.T, ctx context.Context, store *redisstore.Store, cfg Config) {
	t.Helper()
	mbSvc, err := mediabuystore.NewService(store)
	require.NoError(t, err)
	require.NoError(t, mbSvc.Put(ctx, mediabuystore.MediaBuy{
		MediaBuyID:     "mb-smoke",
		SellerAgentURL: cfg.SellerAgentURL,
		StartDate:      "2020-01-01",
		EndDate:        "2099-12-31",
		Packages:       []mediabuystore.MediaBuyPackage{{PackageID: "pkg-smoke", MediaBuyID: "mb-smoke"}},
	}))

	pkgSvc, err := pkgconfigstore.NewService(store)
	require.NoError(t, err)
	require.NoError(t, pkgSvc.Put(ctx, &targeting.PackageContextConfig{
		PackageID: "pkg-smoke",
		Summary:   "smoke-offer",
	}))
}

func mustEncodeRequest(t *testing.T, req *tmproto.ContextMatchRequest) string {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	return string(body)
}

// startValkeyForSmoke spins up a single Valkey 9 container and returns
// the host:port the agent should connect to. Skips the test when
// Docker is unavailable so CI without the daemon doesn't fail.
func startValkeyForSmoke(t *testing.T) string {
	t.Helper()
	if os.Getenv("SKIP_DOCKER_TESTS") != "" {
		t.Skip("SKIP_DOCKER_TESTS set; smoke test needs Docker")
	}
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "valkey/valkey:9-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("Docker not available, skipping smoke test: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("%s:%s", host, port.Port())
}
