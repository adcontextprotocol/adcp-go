package tmproto

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CurrentEpoch returns the daily epoch (days since Unix epoch).
// Used for replay protection: signatures include the epoch, bounding
// replay to ~48 hours (current + previous epoch accepted by verifiers).
func CurrentEpoch() int64 {
	return time.Now().Unix() / 86400
}

// CanonicalizeForSigning creates a deterministic byte representation of the
// static parts of a ContextMatchRequest plus a daily epoch for replay protection.
// Does NOT include request_id (changes per request, enabling signature caching).
// Covers: property_id, property_rid, property_type, placement_id, sorted package_ids, epoch.
func CanonicalizeForSigning(req *ContextMatchRequest, epoch int64) []byte {
	// Length-prefix variable fields to prevent delimiter collision attacks.
	ids := make([]string, len(req.PackageIDs))
	for i, pkgID := range req.PackageIDs {
		ids[i] = fmt.Sprintf("%d:%s", len(pkgID), pkgID)
	}
	sort.Strings(ids)

	payload := fmt.Sprintf("%d:%s|%s|%s|%d:%s|%s|%d",
		len(req.PropertyID), req.PropertyID,
		req.PropertyRID,
		req.PropertyType,
		len(req.PlacementID), req.PlacementID,
		strings.Join(ids, ","),
		epoch,
	)
	return []byte(payload)
}

// SignRequest signs a ContextMatchRequest with the given Ed25519 private key,
// returning a base64url-encoded signature.
func SignRequest(req *ContextMatchRequest, privateKey ed25519.PrivateKey) string {
	payload := CanonicalizeForSigning(req, CurrentEpoch())
	sig := ed25519.Sign(privateKey, payload)
	return base64.RawURLEncoding.EncodeToString(sig)
}

// VerifyRequestSignature verifies a base64url-encoded Ed25519 signature on a
// ContextMatchRequest. Accepts current or previous epoch to handle day boundaries
// (~48h replay window).
func VerifyRequestSignature(req *ContextMatchRequest, b64Sig string, pubKey ed25519.PublicKey) bool {
	sig, err := base64.RawURLEncoding.DecodeString(b64Sig)
	if err != nil {
		return false
	}
	epoch := CurrentEpoch()
	if ed25519.Verify(pubKey, CanonicalizeForSigning(req, epoch), sig) {
		return true
	}
	return ed25519.Verify(pubKey, CanonicalizeForSigning(req, epoch-1), sig)
}
