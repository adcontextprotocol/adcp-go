package contextagent

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// SignRequest signs a ContextMatchRequest with the given private key.
func SignRequest(req *tmproto.ContextMatchRequest, privateKey ed25519.PrivateKey) string {
	return tmproto.SignRequest(req, privateKey)
}

// VerifyRequestSignature verifies the base64-encoded signature on a
// ContextMatchRequest using the publisher's public key from the registry.
func VerifyRequestSignature(req *tmproto.ContextMatchRequest, b64Sig string, registry *PropertyRegistry) error {
	record := registry.Get(req.PropertyRID)
	if record == nil {
		return fmt.Errorf("unknown property rid %d", req.PropertyRID)
	}
	if len(record.PublicKey) == 0 {
		return errors.New("property has no public key")
	}

	sig, err := base64.RawURLEncoding.DecodeString(b64Sig)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}

	epoch := tmproto.CurrentEpoch()
	if ed25519.Verify(record.PublicKey, tmproto.CanonicalizeForSigning(req, epoch), sig) {
		return nil
	}
	if ed25519.Verify(record.PublicKey, tmproto.CanonicalizeForSigning(req, epoch-1), sig) {
		return nil
	}
	return errors.New("invalid signature")
}
