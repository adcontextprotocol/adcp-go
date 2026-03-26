// Package main implements a reference TMP Context Match agent.
//
// The agent evaluates packages against content context using:
// 1. Roaring bitmap pre-filter on property RIDs
// 2. Ed25519 request signature verification
// 3. Property and geo suppression checks
// 4. Modular evaluation pipeline (URL pattern, topic match, etc.)
//
// Property targeting uses Roaring bitmaps for O(1) membership checks across
// 50K+ properties. The registry maps property RIDs to records containing
// domain, public key, and authorized agents.
package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// Agent is the main context match agent.
type Agent struct {
	providerID   string
	registry     *PropertyRegistry
	targeting    *TargetingConfig
	suppressions *SuppressionManager
	modules      []Module
	valkey       ValkeyClient

	// Signature verification sampling. 0 = skip all, 100 = verify every request.
	// Default 5 means ~5% of requests are verified. On first failure, the
	// property is suppressed for 24h and all subsequent requests are verified.
	signatureSampleRate uint32
	requireSignatures   bool   // When true, reject requests with no signature
	verifyCounter       uint64 // atomic counter for sampling
}

// AgentConfig holds configuration for creating a new Agent.
type AgentConfig struct {
	ProviderID          string
	Registry            *PropertyRegistry
	Targeting           *TargetingConfig
	Valkey              ValkeyClient
	Modules             []Module
	SignatureSampleRate uint32 // 0-100, percentage of requests to verify. Default: 5
	RequireSignatures  bool   // When true, reject requests with no signature
}

// NewAgent creates a context match agent with the given configuration.
func NewAgent(cfg AgentConfig) *Agent {
	sampleRate := cfg.SignatureSampleRate
	if sampleRate == 0 {
		sampleRate = 5 // default 5% sampling
	}
	return &Agent{
		providerID:          cfg.ProviderID,
		registry:            cfg.Registry,
		targeting:           cfg.Targeting,
		suppressions:        NewSuppressionManager(cfg.Valkey, cfg.ProviderID),
		modules:             cfg.Modules,
		valkey:              cfg.Valkey,
		signatureSampleRate: sampleRate,
		requireSignatures:   cfg.RequireSignatures,
	}
}

// shouldVerifySignature decides whether to verify this request's signature.
// Uses atomic counter for lock-free sampling. Always verifies if the property
// has no established trust (not in registry or recently failed verification).
func (a *Agent) shouldVerifySignature(rid uint32) bool {
	if a.signatureSampleRate >= 100 {
		return true
	}
	if a.signatureSampleRate == 0 {
		return false
	}
	// Always verify unknown properties
	record := a.registry.Get(rid)
	if record == nil || len(record.PublicKey) == 0 {
		return true
	}
	// Sample established properties
	counter := atomic.AddUint64(&a.verifyCounter, 1)
	return counter%uint64(100/a.signatureSampleRate) == 0
}

// onSignatureFailure handles a failed signature verification by suppressing
// the property and logging the incident.
func (a *Agent) onSignatureFailure(ctx context.Context, rid uint32) {
	// Suppress property for 24 hours
	_ = a.suppressions.SuppressProperty(ctx, rid, 24*time.Hour)
}

// ContextMatch evaluates all available packages against the content context.
// The pipeline is:
//  1. Roaring bitmap pre-filter (is this property in our targeting set?)
//  2. Suppression check (is this property or geo suppressed?)
//  3. Signature verification (is the request authentically from the publisher?)
//  4. Per-package evaluation through the module pipeline
func (a *Agent) ContextMatch(ctx context.Context, req *tmp.ContextMatchRequest) (*tmp.ContextMatchResponse, error) {
	rid := uint32(req.PropertyRID)

	// 1. Bitmap pre-filter: is this property in our targeting set?
	if !a.targeting.ContainsProperty(rid) {
		return emptyResponse(req.RequestID), nil
	}

	// 2. Suppression checks
	suppressed, err := a.suppressions.IsPropertySuppressed(ctx, rid)
	if err != nil {
		return nil, fmt.Errorf("property suppression check: %w", err)
	}
	if suppressed {
		return emptyResponse(req.RequestID), nil
	}

	// 3. Signature verification
	// Reject unsigned requests when signatures are required.
	// Otherwise, sample-verify ~N% of signed requests.
	if a.requireSignatures && req.Signature == "" {
		return nil, fmt.Errorf("signature required but not present for property %d", rid)
	}
	if a.shouldVerifySignature(rid) && req.Signature != "" {
		if err := VerifyRequestSignature(req, req.Signature, a.registry); err != nil {
			a.onSignatureFailure(ctx, rid)
			return nil, fmt.Errorf("signature verification failed for property %d: %w", rid, err)
		}
	}

	// 4. Run module pipeline per package
	var offers []tmp.Offer
	for _, pkg := range req.AvailablePkgs {
		// Check per-package targeting bitmap
		if !a.targeting.ContainsPackageProperty(pkg.PackageID, rid) {
			continue
		}

		activate := true
		var bestScore float32
		for _, mod := range a.modules {
			results := mod.Evaluate(ctx, req, []tmp.AvailablePackage{pkg})
			for _, r := range results {
				if !r.Activate {
					activate = false
					break
				}
				if r.Score > bestScore {
					bestScore = r.Score
				}
			}
			if !activate {
				break
			}
		}
		if activate {
			offers = append(offers, tmp.Offer{PackageID: pkg.PackageID})
		}
	}

	return &tmp.ContextMatchResponse{
		RequestID: req.RequestID,
		Offers:    offers,
	}, nil
}

// VerifyRequest verifies the base64-encoded Ed25519 signature on a request
// using the publisher's public key from the registry.
func (a *Agent) VerifyRequest(req *tmp.ContextMatchRequest, b64Sig string) error {
	return VerifyRequestSignature(req, b64Sig, a.registry)
}

func emptyResponse(requestID string) *tmp.ContextMatchResponse {
	return &tmp.ContextMatchResponse{
		RequestID: requestID,
		Offers:    nil,
	}
}
