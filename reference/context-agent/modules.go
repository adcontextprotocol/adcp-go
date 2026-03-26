package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// Module is a pluggable evaluation step in the context match pipeline.
// Each module evaluates a request against a set of packages and returns
// a list of results indicating whether to activate and at what score.
type Module interface {
	Name() string
	Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult
}

// ModuleResult is the output of a single module evaluation for one package.
type ModuleResult struct {
	PackageID string
	Activate  bool
	Score     float32 // 0-1, used for ranking
	Offer     *tmp.Offer
}

// PropertyListModule checks the Roaring bitmap pre-filter per package.
type PropertyListModule struct {
	targeting *TargetingConfig
}

func NewPropertyListModule(targeting *TargetingConfig) *PropertyListModule {
	return &PropertyListModule{targeting: targeting}
}

func (m *PropertyListModule) Name() string { return "property_list" }

func (m *PropertyListModule) Evaluate(_ context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	rid := uint32(req.PropertyRID)
	results := make([]ModuleResult, 0, len(packages))
	for _, pkg := range packages {
		activate := m.targeting.ContainsPackageProperty(pkg.PackageID, rid)
		score := float32(0)
		if activate {
			score = 1.0
		}
		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}

// URLPatternModule matches artifact IDs or URLs against configured allowlists
// and blocklists stored in Valkey. URLs are hashed (SHA-256) before lookup.
type URLPatternModule struct {
	valkey ValkeyClient
}

func NewURLPatternModule(valkey ValkeyClient) *URLPatternModule {
	return &URLPatternModule{valkey: valkey}
}

func (m *URLPatternModule) Name() string { return "url_pattern" }

func (m *URLPatternModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))
	for _, pkg := range packages {
		activate := true
		for _, artifact := range req.Artifacts {
			urlHash := hashURL(artifact)

			// Check blocklist first
			blocked, _ := m.valkey.SIsMember(ctx, "url:blocklist:"+pkg.PackageID, urlHash)
			if blocked {
				activate = false
				break
			}

			// Check allowlist (if allowlist exists and URL is not in it, reject)
			allowlistKey := "url:allowlist:" + pkg.PackageID
			exists, _ := m.valkey.Exists(ctx, allowlistKey)
			if exists {
				allowed, _ := m.valkey.SIsMember(ctx, allowlistKey, urlHash)
				if !allowed {
					activate = false
					break
				}
			}
		}
		score := float32(0)
		if activate {
			score = 1.0
		}
		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}

// TopicMatchModule checks if artifact topic IDs overlap with package targeting
// topics. Topics are stored as Valkey SETs.
type TopicMatchModule struct {
	valkey ValkeyClient
}

func NewTopicMatchModule(valkey ValkeyClient) *TopicMatchModule {
	return &TopicMatchModule{valkey: valkey}
}

func (m *TopicMatchModule) Name() string { return "topic_match" }

func (m *TopicMatchModule) Evaluate(ctx context.Context, req *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))
	for _, pkg := range packages {
		activate := false
		var bestScore float32

		for _, artifact := range req.Artifacts {
			intersection, _ := m.valkey.SInter(ctx, "topics:package:"+pkg.PackageID, "topics:artifact:"+artifact)
			if len(intersection) > 0 {
				activate = true
				score := float32(len(intersection)) / 10.0
				if score > 1.0 {
					score = 1.0
				}
				if score > bestScore {
					bestScore = score
				}
			}
		}

		// No artifacts means pass through
		if len(req.Artifacts) == 0 {
			activate = true
			bestScore = 0.5
		}

		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     bestScore,
		})
	}
	return results
}

func hashURL(url string) string {
	h := sha256.Sum256([]byte(strings.ToLower(url)))
	return hex.EncodeToString(h[:])
}
