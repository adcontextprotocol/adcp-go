package nitro

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	tee "github.com/adcontextprotocol/adcp-go/tee-attestation"
)

// EmitRequest holds the inputs the emit path needs — the nonce the verifier
// challenged the router with, and the router's in-enclave signing key.
type EmitRequest struct {
	// Nonce is the raw bytes the verifier supplied on the fetch. Must be
	// 16-32 bytes per the spec.
	Nonce []byte

	// SigningKey is the router's per-provider signing key, expressed as a
	// JWK. Only OKP/Ed25519 is supported in this prototype (matches the
	// existing TMP signing envelope in specification.mdx).
	SigningKey tee.JWK

	// ExpiresAt is the router's suggested freshness ceiling. Verifiers may
	// enforce shorter via `attestation_requirement.min_freshness_sec`.
	ExpiresAt time.Time
}

// Emit produces a signed envelope by calling the Nsm with fields projected
// per PROJECTION.md — Nitro `nonce` carries the raw nonce; Nitro `public_key`
// carries the raw Ed25519 public-key bytes.
func Emit(ctx context.Context, nsm Nsm, req EmitRequest) (tee.Envelope, error) {
	if len(req.Nonce) < 16 || len(req.Nonce) > 32 {
		return tee.Envelope{}, fmt.Errorf("nitro emit: nonce must be 16-32 raw bytes, got %d", len(req.Nonce))
	}
	pub, err := req.SigningKey.Ed25519PublicKey()
	if err != nil {
		return tee.Envelope{}, fmt.Errorf("nitro emit: signing_key: %w", err)
	}
	// See PROJECTION.md for why nonce lands in Nitro.nonce and the raw
	// pubkey lands in Nitro.public_key. user_data is intentionally unused
	// in v1 — reserved for a later extension.
	doc, err := nsm.Attest(ctx, AttestRequest{
		Nonce:     req.Nonce,
		PublicKey: pub,
	})
	if err != nil {
		return tee.Envelope{}, fmt.Errorf("nitro emit: NSM Attest: %w", err)
	}
	return tee.Envelope{
		Format:     tee.FormatAWSNitroCOSESign1V1,
		Document:   base64.RawURLEncoding.EncodeToString(doc),
		Nonce:      base64.RawURLEncoding.EncodeToString(req.Nonce),
		SigningKey: req.SigningKey,
		ExpiresAt:  req.ExpiresAt,
	}, nil
}
