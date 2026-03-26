package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// currentEpoch returns the daily epoch (days since Unix epoch).
func currentEpoch() int64 {
	return time.Now().Unix() / 86400
}

// canonicalizeRequest produces a deterministic byte representation matching
// the router's canonicalizeForSigning format. Uses pipe-delimited static
// fields + daily epoch. Does NOT include request_id (enables signature caching).
func canonicalizeRequest(req *tmp.ContextMatchRequest, epoch int64) []byte {
	ids := make([]string, len(req.AvailablePkgs))
	for i, p := range req.AvailablePkgs {
		ids[i] = p.PackageID
	}
	sort.Strings(ids)

	payload := fmt.Sprintf("%s|%d|%s|%s|%s|%d",
		req.PropertyID,
		req.PropertyRID,
		req.PropertyType,
		req.PlacementID,
		strings.Join(ids, ","),
		epoch,
	)
	return []byte(payload)
}

// SignRequest signs a ContextMatchRequest with the given private key,
// returning a base64-encoded signature string.
func SignRequest(req *tmp.ContextMatchRequest, privateKey ed25519.PrivateKey) string {
	payload := canonicalizeRequest(req, currentEpoch())
	sig := ed25519.Sign(privateKey, payload)
	return base64.RawURLEncoding.EncodeToString(sig)
}

// VerifyRequestSignature verifies the base64-encoded signature on a
// ContextMatchRequest using the publisher's public key from the registry.
// Accepts current or previous epoch to handle day boundaries.
func VerifyRequestSignature(req *tmp.ContextMatchRequest, b64Sig string, registry *PropertyRegistry) error {
	rid := uint32(req.PropertyRID)
	record := registry.Get(rid)
	if record == nil {
		return fmt.Errorf("unknown property rid %d", rid)
	}
	if len(record.PublicKey) == 0 {
		return errors.New("property has no public key")
	}

	sig, err := base64.RawURLEncoding.DecodeString(b64Sig)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	epoch := currentEpoch()
	// Try current epoch first
	if ed25519.Verify(record.PublicKey, canonicalizeRequest(req, epoch), sig) {
		return nil
	}
	// Try previous epoch (handles day boundary)
	if ed25519.Verify(record.PublicKey, canonicalizeRequest(req, epoch-1), sig) {
		return nil
	}
	return errors.New("invalid signature")
}
