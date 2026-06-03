package contextagent

import (
	"context"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/targeting/mediabuystore"
	"github.com/adcontextprotocol/adcp-go/targeting/pkgconfigstore"
	"github.com/adcontextprotocol/adcp-go/targeting/suppressionstore"
	"github.com/adcontextprotocol/adcp-go/targeting/topicstore"
	"github.com/adcontextprotocol/adcp-go/targeting/urlliststore"
)

// storage adapts the per-domain Reader interfaces into a single
// targeting.ContextStorage the engine consumes. Each field is set by
// buildBundle to either a direct reader or a cache-wrapped reader,
// depending on the master + per-domain enable flags in CacheConfig.
type storage struct {
	mediaBuys     mediabuystore.Reader
	pkgConfigs    pkgconfigstore.Reader
	urlLists      urlliststore.Reader
	topics        *topicstore.Reader
	suppressions  *suppressionstore.Snapshot
	acceptedTaxes []topicstore.Taxonomy
}

func (s *storage) ActivePackages(ctx context.Context, sellerAgentURL, propertyID, country, placementID string, now time.Time) ([]string, error) {
	pkgs, err := s.mediaBuys.ActivePackages(ctx, sellerAgentURL, propertyID, country, placementID, now)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(pkgs))
	seen := make(map[string]struct{}, len(pkgs))
	for _, p := range pkgs {
		if _, ok := seen[p.PackageID]; ok {
			continue
		}
		seen[p.PackageID] = struct{}{}
		out = append(out, p.PackageID)
	}
	return out, nil
}

func (s *storage) ContextConfig(ctx context.Context, packageID string) (*targeting.PackageContextConfig, bool, error) {
	return s.pkgConfigs.Get(ctx, packageID)
}

func (s *storage) ArtifactTopics(ctx context.Context, tax topicstore.Taxonomy, ref string) ([]string, error) {
	return s.topics.ArtifactTopics(ctx, tax, ref)
}

func (s *storage) PackageTopics(ctx context.Context, tax topicstore.Taxonomy, packageID string) ([]string, error) {
	return s.topics.PackageTopics(ctx, tax, packageID)
}

func (s *storage) URLBlocked(ctx context.Context, packageID, urlHash string) (bool, error) {
	return s.urlLists.IsBlocked(ctx, packageID, urlHash)
}

func (s *storage) URLAllowed(ctx context.Context, packageID, urlHash string) (bool, error) {
	return s.urlLists.IsAllowed(ctx, packageID, urlHash)
}

func (s *storage) IsPropertySuppressed(ctx context.Context, providerID, propertyRID string) (bool, error) {
	return s.suppressions.IsPropertySuppressed(ctx, providerID, propertyRID)
}

func (s *storage) IsGeoSuppressed(ctx context.Context, providerID, country string) (bool, error) {
	return s.suppressions.IsGeoSuppressed(ctx, providerID, country)
}

// buildStorage assembles a storage from the supplied per-domain stores
// and cache configuration. Caches activate only when the master switch
// AND the per-domain switch are both true.
func buildStorage(
	mediaBuyStore mediabuystore.Store,
	pkgConfigStore pkgconfigstore.Store,
	urlListStore urlliststore.Store,
	topicStore topicstore.Store,
	suppressSnap *suppressionstore.Snapshot,
	taxes []topicstore.Taxonomy,
	cacheCfg CacheConfig,
) (*storage, error) {
	mediaBuys := mediabuystore.NewReader(mediaBuyStore)
	if cacheCfg.Enabled && cacheCfg.MediaBuy.Enabled {
		mediaBuys = mediabuystore.WithCache(mediaBuys, mediabuystore.CacheConfig{
			SellerSetSize: cacheCfg.MediaBuy.SellerSetSize,
			SellerSetTTL:  cacheCfg.MediaBuy.SellerSetTTL,
			MediaBuySize:  cacheCfg.MediaBuy.MediaBuySize,
			MediaBuyTTL:   cacheCfg.MediaBuy.MediaBuyTTL,
		})
	}

	pkgConfigs := pkgconfigstore.NewReader(pkgConfigStore)
	if cacheCfg.Enabled && cacheCfg.PkgConfig.Enabled {
		pkgConfigs = pkgconfigstore.WithCache(pkgConfigs, pkgconfigstore.CacheConfig{
			Size: cacheCfg.PkgConfig.Size,
			TTL:  cacheCfg.PkgConfig.TTL,
		})
	}

	urlLists := urlliststore.NewReader(urlListStore)
	if cacheCfg.Enabled && cacheCfg.URLList.Enabled {
		urlLists = urlliststore.WithCache(urlLists, urlliststore.CacheConfig{
			Size: cacheCfg.URLList.Size,
			TTL:  cacheCfg.URLList.TTL,
		})
	}

	topics, err := topicstore.NewReader(topicStore)
	if err != nil {
		return nil, err
	}
	if cacheCfg.Enabled && cacheCfg.Topics.Enabled {
		topics = topicstore.WithCache(topics, topicstore.CacheConfig{
			ArtifactSize: cacheCfg.Topics.ArtifactSize,
			ArtifactTTL:  cacheCfg.Topics.ArtifactTTL,
			PackageSize:  cacheCfg.Topics.PackageSize,
			PackageTTL:   cacheCfg.Topics.PackageTTL,
		})
	}

	return &storage{
		mediaBuys:     mediaBuys,
		pkgConfigs:    pkgConfigs,
		urlLists:      urlLists,
		topics:        topics,
		suppressions:  suppressSnap,
		acceptedTaxes: taxes,
	}, nil
}
