package contextagent

import (
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseValidConfig returns a Config that passes Validate. Tests mutate
// one field at a time to assert the rule under test fires in isolation
// without other validate errors crowding the output.
func baseValidConfig() Config {
	return Config{
		HTTPPort:                   8081,
		RequestTimeout:             40 * time.Millisecond,
		HTTPReadHeaderTimeout:      200 * time.Millisecond,
		HTTPReadTimeout:            500 * time.Millisecond,
		HTTPWriteTimeout:           1 * time.Second,
		HTTPIdleTimeout:            30 * time.Second,
		ShutdownGrace:              time.Second,
		ShutdownTimeout:            10 * time.Second,
		RequestBodyLimitBytes:      256 * 1024,
		MaxHeaderBytes:             8 * 1024,
		MaxOpenConnections:         1024,
		ResponseTTL:                time.Minute,
		ProviderID:                 "provider-1",
		SellerAgentURL:             "https://seller.example.com/agent",
		AcceptedTaxonomies:         []topicstore.Taxonomy{{Source: "iab", ID: 7}},
		SuppressionRefreshInterval: 5 * time.Minute,
		SupportedADCPMajorVersions: []int{3},
		TMP: TMPConfig{
			RegistryURL:    "https://router.example/registry/snapshot",
			OwnEndpointURL: "https://context.example/context",
		},
		Valkey: ValkeyBlock{
			Enabled: true,
			Mode:    "standalone",
			Shards:  map[string]string{"0": "valkey:6379"},
		},
		Cache: CacheConfig{
			Enabled: true,
			MediaBuy: MediaBuyCacheConfig{
				Enabled:       true,
				SellerSetSize: 1024, SellerSetTTL: time.Minute,
				MediaBuySize: 1024, MediaBuyTTL: time.Minute,
			},
			PkgConfig: DomainCacheConfig{Enabled: true, Size: 1024, TTL: time.Minute},
			URLList:   DomainCacheConfig{Enabled: true, Size: 1024, TTL: time.Minute},
			Topics: TopicsCacheConfig{
				Enabled:      true,
				ArtifactSize: 1024, ArtifactTTL: time.Minute,
				PackageSize: 1024, PackageTTL: time.Minute,
			},
		},
	}
}

func TestConfigValidate_Baseline(t *testing.T) {
	require.NoError(t, baseValidConfig().Validate())
}

func TestConfigValidate_RequiredFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		errMsg string
	}{
		{"missing provider_id", func(c *Config) { c.ProviderID = "" }, "PROVIDER_ID"},
		{"missing seller_agent_url", func(c *Config) { c.SellerAgentURL = "" }, "SELLER_AGENT_URL"},
		{"missing tmp registry url", func(c *Config) { c.TMP.RegistryURL = "" }, "TMP_REGISTRY_URL"},
		{"missing tmp own endpoint", func(c *Config) { c.TMP.OwnEndpointURL = "" }, "TMP_OWN_ENDPOINT_URL"},
		{"missing valkey shards", func(c *Config) {
			c.Valkey.Enabled = false
			c.Valkey.Shards = nil
		}, "VALKEY_SHARDS"},
		{"non-positive suppression refresh", func(c *Config) { c.SuppressionRefreshInterval = 0 }, "SUPPRESSION_REFRESH_INTERVAL"},
		{"non-positive shutdown timeout", func(c *Config) { c.ShutdownTimeout = 0 }, "SHUTDOWN_TIMEOUT"},
		{"empty adcp versions", func(c *Config) { c.SupportedADCPMajorVersions = nil }, "SUPPORTED_ADCP_MAJOR_VERSIONS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

func TestConfigValidate_TimeoutRelations(t *testing.T) {
	t.Run("write less than request", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.HTTPWriteTimeout = 10 * time.Millisecond // shorter than RequestTimeout=40ms
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP_WRITE_TIMEOUT must be >= REQUEST_TIMEOUT")
	})
	t.Run("read less than read-header", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.HTTPReadTimeout = 50 * time.Millisecond // shorter than HTTPReadHeaderTimeout=200ms
		err := cfg.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP_READ_TIMEOUT must be >= HTTP_READ_HEADER_TIMEOUT")
	})
}

func TestConfigValidate_PprofRequiresAdminPort(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Pprof.Enabled = true
	cfg.AdminPort = 0
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ADMIN_PORT")
	assert.Contains(t, err.Error(), "pprof")
}

func TestConfigValidate_PprofOKWithAdminPort(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Pprof.Enabled = true
	cfg.AdminPort = 9090
	require.NoError(t, cfg.Validate())
}

func TestConfigValidate_CacheSizesRequiredWhenEnabled(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		errMsg string
	}{
		{"mediabuy seller set", func(c *Config) {
			c.Cache.MediaBuy.SellerSetSize = 0
		}, "CACHE_MEDIABUY_SELLER_SIZE"},
		{"mediabuy record", func(c *Config) {
			c.Cache.MediaBuy.MediaBuySize = 0
		}, "CACHE_MEDIABUY_RECORD_SIZE"},
		{"pkgconfig", func(c *Config) {
			c.Cache.PkgConfig.Size = 0
		}, "CACHE_PKGCONFIG_SIZE"},
		{"urllist", func(c *Config) {
			c.Cache.URLList.Size = 0
		}, "CACHE_URLLIST_SIZE"},
		{"topics artifact", func(c *Config) {
			c.Cache.Topics.ArtifactSize = 0
		}, "CACHE_TOPICS_ARTIFACT_SIZE"},
		{"topics package", func(c *Config) {
			c.Cache.Topics.PackageSize = 0
		}, "CACHE_TOPICS_PACKAGE_SIZE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errMsg)
		})
	}
}

func TestConfigValidate_CacheSizesIgnoredWhenDisabled(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Cache.MediaBuy.Enabled = false
	cfg.Cache.MediaBuy.SellerSetSize = 0 // size 0 OK because cache disabled
	cfg.Cache.MediaBuy.MediaBuySize = 0
	require.NoError(t, cfg.Validate())
}

func TestConfigValidate_AllowUnsignedSkipsTMPRequirements(t *testing.T) {
	cfg := baseValidConfig()
	cfg.TMP.AllowUnsigned = true
	cfg.TMP.RegistryURL = ""
	cfg.TMP.OwnEndpointURL = ""
	require.NoError(t, cfg.Validate())
}

func TestLookupTaxonomies(t *testing.T) {
	cases := []struct {
		raw  string
		want []topicstore.Taxonomy
		ok   bool
	}{
		{"", nil, true},
		{"iab:7", []topicstore.Taxonomy{{Source: "iab", ID: 7}}, true},
		{"iab:7,custom_v1:1", []topicstore.Taxonomy{{Source: "iab", ID: 7}, {Source: "custom_v1", ID: 1}}, true},
		{"iab:", nil, false},
		{":7", nil, false},
		{"no-colon", nil, false},
		{"iab:not-an-int", nil, false},
		{"ia/b:1", nil, false}, // Taxonomy.Validate rejects slash
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("TEST_ACCEPTED_TAXONOMIES", tc.raw)
			got, err := lookupTaxonomies("TEST_ACCEPTED_TAXONOMIES")
			if tc.ok {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
