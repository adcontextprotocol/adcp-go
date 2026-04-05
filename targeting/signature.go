package targeting

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// PropertyRegistry provides public keys for signature verification.
type PropertyRegistry interface {
	GetPublicKey(rid uint64) ed25519.PublicKey
}

// shouldVerifySignature decides whether to verify a request's signature.
// Always verifies unknown properties. Otherwise, samples at the configured rate.
func (e *Engine) shouldVerifySignature(rid uint64) bool {
	if e.sigSampleRate == 0 {
		return false
	}
	if e.sigSampleRate >= 100 {
		return true
	}
	if e.registry == nil {
		return false
	}
	// Always verify unknown properties.
	pk := e.registry.GetPublicKey(rid)
	if len(pk) == 0 {
		return true
	}
	// counter%100 < N gives exactly N% sampling.
	counter := e.sigCounter.Add(1)
	return counter%100 < uint64(e.sigSampleRate)
}

// verifySignature checks the request signature if verification is warranted.
func (e *Engine) verifySignature(ctx context.Context, req *tmproto.ContextMatchRequest) error {
	if e.requireSignatures && req.Signature == "" {
		return fmt.Errorf("signature required but not present for property %d", req.PropertyRID)
	}
	if e.registry == nil {
		return nil
	}
	if !e.shouldVerifySignature(req.PropertyRID) || req.Signature == "" {
		return nil
	}

	pk := e.registry.GetPublicKey(req.PropertyRID)
	if len(pk) == 0 {
		return fmt.Errorf("unknown property rid %d", req.PropertyRID)
	}

	sig, err := base64.RawURLEncoding.DecodeString(req.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	epoch := tmproto.CurrentEpoch()
	if ed25519.Verify(pk, tmproto.CanonicalizeForSigning(req, epoch), sig) {
		return nil
	}
	if ed25519.Verify(pk, tmproto.CanonicalizeForSigning(req, epoch-1), sig) {
		return nil
	}

	// Suppress property on failure.
	_ = e.SuppressProperty(ctx, req.PropertyRID, signatureFailureTTL)
	return errors.New("invalid signature")
}
