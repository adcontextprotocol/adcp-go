package nitro

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/veraison/go-cose"
)

// MockNsm generates real COSE_Sign1 Nitro attestation documents signed by a
// caller-owned test CA. Same wire format as a production Nitro document,
// which means the verifier code path is exercised identically between the
// mock and a real Nitro instance — including COSE_Sign1 parsing, X.509
// chain verification, ECDSA-P384 signature verification, PCR extraction,
// and the binding rule.
//
// The mock is NOT a security boundary. Its RootCert is public in whatever
// tree it lives in; verifiers built against it are only useful in tests.
// Production verifiers must be constructed with the real AWS Nitro Root CA
// (see verify.go's Verifier struct).
type MockNsm struct {
	// RootCert and RootKey are the CA the mock uses to sign the NSM
	// certificate. Verifiers use RootCert as the trust anchor.
	RootCert *x509.Certificate
	RootKey  *ecdsa.PrivateKey

	// NsmCert and NsmKey are the "NSM" leaf that signs COSE_Sign1
	// documents. Chains up to RootCert. Regenerated per MockNsm to keep
	// runs independent.
	NsmCert *x509.Certificate
	NsmKey  *ecdsa.PrivateKey

	// ModuleID is echoed into every document. Fixed per-mock so tests can
	// assert on it.
	ModuleID string

	// Now returns the current time for timestamps and certificate
	// validity. Test can inject a clock.
	Now func() time.Time

	// PCRs is the mock PCR set every document reports. Tests can point
	// this at whatever measurements they want to exercise the allowlist
	// against.
	PCRs map[uint32][]byte
}

// NewMockNsm builds a MockNsm with a fresh CA and NSM leaf. PCRs default to
// a stable set of "audited router" placeholders; override to test the
// allowlist code path.
func NewMockNsm() (*MockNsm, error) {
	now := time.Now
	rootKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mock: gen root key: %w", err)
	}
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TMP Router Attestation Mock Root CA"},
		NotBefore:             now().Add(-time.Hour),
		NotAfter:              now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("mock: create root cert: %w", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, fmt.Errorf("mock: parse root cert: %w", err)
	}

	nsmKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("mock: gen NSM key: %w", err)
	}
	nsmTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Mock NSM"},
		NotBefore:    now().Add(-time.Hour),
		NotAfter:     now().Add(12 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}
	nsmDER, err := x509.CreateCertificate(rand.Reader, nsmTmpl, rootCert, &nsmKey.PublicKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("mock: create NSM cert: %w", err)
	}
	nsmCert, err := x509.ParseCertificate(nsmDER)
	if err != nil {
		return nil, fmt.Errorf("mock: parse NSM cert: %w", err)
	}

	return &MockNsm{
		RootCert: rootCert,
		RootKey:  rootKey,
		NsmCert:  nsmCert,
		NsmKey:   nsmKey,
		ModuleID: "mock-nsm",
		Now:      now,
		PCRs:     defaultMockPCRs(),
	}, nil
}

// Attest produces a real COSE_Sign1 Nitro document that verifies against
// MockNsm.RootCert. Same wire format as a production Nitro document.
func (m *MockNsm) Attest(ctx context.Context, req AttestRequest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("mock: nil MockNsm")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(req.Nonce) > 512 {
		return nil, fmt.Errorf("mock: nonce %d bytes exceeds 512", len(req.Nonce))
	}
	if len(req.PublicKey) > 1024 {
		return nil, fmt.Errorf("mock: public_key %d bytes exceeds 1024", len(req.PublicKey))
	}
	if len(req.UserData) > 512 {
		return nil, fmt.Errorf("mock: user_data %d bytes exceeds 512", len(req.UserData))
	}

	doc := Document{
		ModuleID:    m.ModuleID,
		Timestamp:   uint64(m.Now().UnixMilli()),
		Digest:      "SHA384",
		PCRs:        m.PCRs,
		Certificate: m.NsmCert.Raw,
		CABundle:    [][]byte{m.RootCert.Raw},
		PublicKey:   req.PublicKey,
		UserData:    req.UserData,
		Nonce:       req.Nonce,
	}
	payload, err := doc.MarshalCBOR()
	if err != nil {
		return nil, err
	}

	// Wrap in COSE_Sign1 with ES384 (matching real Nitro NSM). The
	// veraison/go-cose library's Signer interface uses an internal Algorithm
	// enum — cose.AlgorithmES384 for ECDSA P-384 + SHA-384.
	signer, err := cose.NewSigner(cose.AlgorithmES384, m.NsmKey)
	if err != nil {
		return nil, fmt.Errorf("mock: build COSE signer: %w", err)
	}
	msg := cose.NewSign1Message()
	msg.Headers.Protected.SetAlgorithm(cose.AlgorithmES384)
	msg.Payload = payload
	if err := msg.Sign(rand.Reader, nil, signer); err != nil {
		return nil, fmt.Errorf("mock: COSE_Sign1 sign: %w", err)
	}
	return msg.MarshalCBOR()
}

// defaultMockPCRs returns a placeholder PCR map that looks structurally like
// what a real Nitro EIF produces: SHA-384 (48 bytes) per register, with the
// first few filled and the rest zeroed. Tests that exercise measurement
// allowlisting should override this via MockNsm.PCRs.
func defaultMockPCRs() map[uint32][]byte {
	pcrs := make(map[uint32][]byte, 16)
	// A stable non-zero PCR0 so an allowlist can key off it. Real PCR0
	// is the SHA-384 of the EIF; here we use a fixed marker so tests
	// don't need to reproduce that derivation.
	pcr0 := sha512.Sum384([]byte("mock-nsm/audited-tmp-router-v0.0.1"))
	pcrs[0] = pcr0[:]
	// PCR1 and PCR2 as documented — SHA-384 of the boot kernel + init
	// respectively. Different fixed markers so a test that wants to
	// deny-on-PCR1 sees a distinguishable value.
	pcr1 := sha512.Sum384([]byte("mock-nsm/kernel"))
	pcrs[1] = pcr1[:]
	pcr2 := sha512.Sum384([]byte("mock-nsm/init"))
	pcrs[2] = pcr2[:]
	for i := uint32(3); i < 16; i++ {
		pcrs[i] = make([]byte, sha256.Size+16) // 48 bytes of zeros
	}
	return pcrs
}

// Ensure MockNsm satisfies Nsm.
var _ Nsm = (*MockNsm)(nil)
