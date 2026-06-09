package tmproto

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestSigner(t *testing.T) (*Signer, *StaticKeyStore) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := NewSigner("test-key-1", priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	ks := NewStaticKeyStore([]SigningKey{PublicSigningKey(signer.KeyID, pub)})
	return signer, ks
}

func TestSignerContextMatchRoundtrip(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"

	req := &ContextMatchRequest{
		RequestID:   "req-1",
		PropertyRID: "11111111-1111-1111-1111-111111111111",
		PropertyID:  "publisher_homepage",
		PlacementID: "main_top",
		PackageIDs:  []string{"pkg-b", "pkg-a"},
	}
	sig := signer.SignContextMatch(req, endpoint, EpochAt(now))
	if err := VerifyContextMatch(req, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("verify same epoch: %v", err)
	}
}

func TestSignerContextMatchTrailingSlashCompat(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Now()
	// Signer endpoint has trailing slash, verifier doesn't — both should
	// normalize to the same value.
	signerURL := "https://provider.example.com/"
	verifierURL := "https://provider.example.com"
	req := &ContextMatchRequest{
		RequestID:   "r",
		PropertyRID: "p",
		PlacementID: "pl",
	}
	sig := signer.SignContextMatch(req, signerURL, EpochAt(now))
	if err := VerifyContextMatch(req, verifierURL, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("trailing-slash mismatch should normalize: %v", err)
	}
}

func TestSignerContextMatchWrongEndpointRejected(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Now()
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	sig := signer.SignContextMatch(req, "https://provider-a.example.com", EpochAt(now))
	err := VerifyContextMatch(req, "https://provider-b.example.com", sig, signer.KeyID, ks, now)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for endpoint mismatch, got %v", err)
	}
}

func TestSignerContextMatchPreviousEpochAccepted(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	sig := signer.SignContextMatch(req, endpoint, EpochAt(now)-1)
	if err := VerifyContextMatch(req, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("previous epoch should verify: %v", err)
	}
}

func TestSignerContextMatchTooOldRejected(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	sig := signer.SignContextMatch(req, endpoint, EpochAt(now)-2)
	err := VerifyContextMatch(req, endpoint, sig, signer.KeyID, ks, now)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for stale epoch, got %v", err)
	}
}

func TestSignerContextMatchPackageIDsSorted(t *testing.T) {
	// Different insertion orders must produce identical signing inputs.
	a := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl", PackageIDs: []string{"c", "a", "b"}}
	b := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl", PackageIDs: []string{"a", "b", "c"}}
	endpoint := "https://provider.example.com"
	epoch := int64(20000)
	ia := BuildContextMatchSigningInput(a, endpoint, epoch)
	ib := BuildContextMatchSigningInput(b, endpoint, epoch)
	if string(ia) != string(ib) {
		t.Fatalf("package_ids order must not change signing input:\n%q\nvs\n%q", ia, ib)
	}
}

func TestSignerContextMatchEmptyPackageIDs(t *testing.T) {
	req := &ContextMatchRequest{RequestID: "r", SellerAgentURL: "https://seller.example.com/agent", PropertyRID: "p", PlacementID: "pl"}
	endpoint := "https://provider.example.com"
	got := string(BuildContextMatchSigningInput(req, endpoint, 20000))
	want := strings.Join([]string{
		"context_match_request",
		"https://seller.example.com/agent",
		"p",
		"pl",
		"",
		"https://provider.example.com",
		"20000",
	}, "\n")
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestSignerIdentityMatchRoundtrip(t *testing.T) {
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"
	req := &IdentityMatchRequest{
		RequestID:      "req-id-1",
		SellerAgentURL: "https://seller.example.com/agent",
		Identities: []IdentityToken{
			{UIDType: UIDTypeUID2, UserToken: "tok_b"},
			{UIDType: UIDTypeID5, UserToken: "tok_a"},
		},
		Consent:    map[string]any{"tcf_consent": "CO123"},
		PackageIDs: []string{"pkg-x", "pkg-y"},
	}
	sig, err := signer.SignIdentityMatch(req, endpoint, EpochAt(now))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := VerifyIdentityMatch(req, endpoint, sig, signer.KeyID, ks, now); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestSignerIdentityMatchPerProviderBinding(t *testing.T) {
	// A signature minted for provider A must NOT verify when replayed against
	// provider B — even with the same body.
	signer, ks := newTestSigner(t)
	now := time.Now()
	req := &IdentityMatchRequest{
		RequestID:  "r",
		Identities: []IdentityToken{{UIDType: UIDTypeUID2, UserToken: "tok"}},
		PackageIDs: []string{"pkg"},
	}
	sig, err := signer.SignIdentityMatch(req, "https://provider-a.example.com", EpochAt(now))
	if err != nil {
		t.Fatal(err)
	}
	err = VerifyIdentityMatch(req, "https://provider-b.example.com", sig, signer.KeyID, ks, now)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid for provider replay, got %v", err)
	}
}

func TestSignerIdentityMatchIdentityOrderIndependent(t *testing.T) {
	a := &IdentityMatchRequest{
		RequestID: "r",
		Identities: []IdentityToken{
			{UIDType: UIDTypeID5, UserToken: "tok_a"},
			{UIDType: UIDTypeUID2, UserToken: "tok_b"},
		},
		PackageIDs: []string{"x"},
	}
	b := &IdentityMatchRequest{
		RequestID: "r",
		Identities: []IdentityToken{
			{UIDType: UIDTypeUID2, UserToken: "tok_b"},
			{UIDType: UIDTypeID5, UserToken: "tok_a"},
		},
		PackageIDs: []string{"x"},
	}
	endpoint := "https://provider.example.com"
	ia, err := BuildIdentityMatchSigningInput(a, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := BuildIdentityMatchSigningInput(b, endpoint, 20000)
	if err != nil {
		t.Fatal(err)
	}
	if string(ia) != string(ib) {
		t.Fatalf("identity order must not change signing input")
	}
}

func TestSignerIdentityMatchSellerAgentURLBound(t *testing.T) {
	// A signature minted for one seller_agent_url must NOT verify when replayed
	// for a request whose seller_agent_url differs — the field is part of the
	// canonical signing input.
	signer, ks := newTestSigner(t)
	now := time.Unix(1_700_000_000, 0)
	endpoint := "https://provider.example.com"
	base := &IdentityMatchRequest{
		RequestID:      "r",
		SellerAgentURL: "https://seller-a.example.com/agent",
		Identities:     []IdentityToken{{UIDType: UIDTypeUID2, UserToken: "tok"}},
		PackageIDs:     []string{"pkg"},
	}
	sig, err := signer.SignIdentityMatch(base, endpoint, EpochAt(now))
	if err != nil {
		t.Fatal(err)
	}

	swapped := *base
	swapped.SellerAgentURL = "https://seller-b.example.com/agent"
	if err := VerifyIdentityMatch(&swapped, endpoint, sig, signer.KeyID, ks, now); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid when seller_agent_url is swapped, got %v", err)
	}
}

func TestSignerIdentityMatchDeduplicatesIdentities(t *testing.T) {
	a := &IdentityMatchRequest{
		RequestID: "r",
		Identities: []IdentityToken{
			{UIDType: UIDTypeUID2, UserToken: "tok"},
		},
		PackageIDs: []string{"x"},
	}
	b := &IdentityMatchRequest{
		RequestID: "r",
		Identities: []IdentityToken{
			{UIDType: UIDTypeUID2, UserToken: "tok"},
			{UIDType: UIDTypeUID2, UserToken: "tok"}, // dup
		},
		PackageIDs: []string{"x"},
	}
	endpoint := "https://provider.example.com"
	ia, _ := BuildIdentityMatchSigningInput(a, endpoint, 20000)
	ib, _ := BuildIdentityMatchSigningInput(b, endpoint, 20000)
	if string(ia) != string(ib) {
		t.Fatalf("duplicate identities must not change signing input")
	}
}

func TestVerifyMissingHeaders(t *testing.T) {
	h := http.Header{}
	if _, _, err := ExtractSignatureHeaders(h); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("expected ErrSignatureMissing, got %v", err)
	}
	h.Set(HeaderTMPSignature, "abc")
	if _, _, err := ExtractSignatureHeaders(h); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("expected ErrSignatureMissing for kid-only-missing, got %v", err)
	}
}

func TestVerifyUnknownKid(t *testing.T) {
	signer, _ := newTestSigner(t)
	emptyKS := NewStaticKeyStore(nil)
	now := time.Now()
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	sig := signer.SignContextMatch(req, "https://x", EpochAt(now))
	err := VerifyContextMatch(req, "https://x", sig, signer.KeyID, emptyKS, now)
	if !errors.Is(err, ErrSignatureKeyUnknown) {
		t.Fatalf("expected ErrSignatureKeyUnknown, got %v", err)
	}
}

func TestVerifyRevokedKeyRejected(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := NewSigner("kid", priv)
	now := time.Unix(1_700_000_000, 0)

	revokedAt := now.Add(-48 * time.Hour) // revoked 2 days ago
	jwk := PublicSigningKey(signer.KeyID, pub)
	jwk.RevokedAt = &revokedAt
	ks := NewStaticKeyStore([]SigningKey{jwk})

	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	sig := signer.SignContextMatch(req, "https://x", EpochAt(now))
	err = VerifyContextMatch(req, "https://x", sig, signer.KeyID, ks, now)
	if !errors.Is(err, ErrSignatureKeyRevoked) {
		t.Fatalf("expected ErrSignatureKeyRevoked, got %v", err)
	}
}

func TestVerifyMalformedSignatureRejected(t *testing.T) {
	_, ks := newTestSigner(t)
	req := &ContextMatchRequest{RequestID: "r", PropertyRID: "p", PlacementID: "pl"}
	leakySignature := "!!!not-base64!!!"
	err := VerifyContextMatch(req, "https://x", leakySignature, "test-key-1", ks, time.Now())
	if !errors.Is(err, ErrSignatureMalformed) {
		t.Fatalf("expected ErrSignatureMalformed, got %v", err)
	}
	if strings.Contains(err.Error(), leakySignature) || strings.Contains(err.Error(), "illegal base64") || strings.Contains(err.Error(), "byte") {
		t.Fatalf("malformed signature error leaked decoder details: %v", err)
	}
}

func TestPublicJWKShape(t *testing.T) {
	signer, _ := newTestSigner(t)
	jwk := signer.PublicJWK()
	if jwk.Kid != signer.KeyID || jwk.Kty != "OKP" || jwk.Crv != "Ed25519" || jwk.Alg != "EdDSA" || jwk.Use != "sig" {
		t.Fatalf("unexpected JWK shape: %+v", jwk)
	}
	pub, err := jwk.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	jwk2 := signer.PublicJWK()
	want, err := jwk2.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey: %v", err)
	}
	if string(pub) != string(want) {
		t.Fatal("derived public key does not roundtrip")
	}
}

func TestNormalizeProviderEndpointURL(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"https://example.com", "https://example.com"},
		{"https://example.com/", "https://example.com"},
		{"https://example.com////", "https://example.com"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeProviderEndpointURL(tc.in); got != tc.out {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.out)
		}
	}
}
