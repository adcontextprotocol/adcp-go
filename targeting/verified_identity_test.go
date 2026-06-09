package targeting

import (
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestVerifiedIdentityCapKeyNamespaced proves the fcap key carries the relying
// party scope: the SAME nullifier under two different relying parties yields
// distinct cap keys (no cross-RP cap contamination), and every key is prefixed
// so it cannot collide with a raw UserToken cap key (no cap-poisoning by
// sending a guessed nullifier as an ordinary token).
func TestVerifiedIdentityCapKeyNamespaced(t *testing.T) {
	const nullifier = "0xNULLIFIER"
	a := VerifiedIdentity{Nullifier: nullifier, RelyingPartyID: "rp-a"}
	b := VerifiedIdentity{Nullifier: nullifier, RelyingPartyID: "rp-b"}

	if a.CapKey() == b.CapKey() {
		t.Fatalf("same nullifier under different RPs must produce distinct cap keys; both = %q", a.CapKey())
	}
	if a.CapKey() == nullifier {
		t.Fatalf("cap key must be namespaced, not the raw nullifier (got %q)", a.CapKey())
	}
	for _, vi := range []VerifiedIdentity{a, b} {
		if got := vi.CapKey(); got[:4] != "vid:" {
			t.Errorf("cap key %q must carry the vid: namespace prefix", got)
		}
	}
}

func TestLocalPreCheck(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	base := func() *tmproto.Attestation {
		return &tmproto.Attestation{
			Scheme:         "world_id_v4",
			RelyingPartyID: "rp-1",
			SignalBinding:  "binding-committing-to-request",
			Claims:         []tmproto.AttestationClaim{tmproto.AttestationClaimUniqueHuman},
		}
	}
	vctx := VerifyContext{RequestID: "r1", ExpectedRelyingPartyID: "rp-1", Now: now}

	cases := []struct {
		name string
		mut  func(*tmproto.Attestation)
		vctx VerifyContext
		want string
	}{
		{"happy", func(*tmproto.Attestation) {}, vctx, PreCheckOK},
		{"future expiry ok", func(a *tmproto.Attestation) { a.ExpiresAt = future.Format(time.RFC3339) }, vctx, PreCheckOK},
		{"empty signal_binding fails closed", func(a *tmproto.Attestation) { a.SignalBinding = "" }, vctx, PreCheckNoBinding},
		{"expired", func(a *tmproto.Attestation) { a.ExpiresAt = past.Format(time.RFC3339) }, vctx, PreCheckExpired},
		{"unparseable expiry fails closed", func(a *tmproto.Attestation) { a.ExpiresAt = "not-a-date" }, vctx, PreCheckExpired},
		{"rp mismatch", func(a *tmproto.Attestation) { a.RelyingPartyID = "rp-other" }, vctx, PreCheckRPMismatch},
		{"missing rp", func(a *tmproto.Attestation) { a.RelyingPartyID = "" }, vctx, PreCheckRPMismatch},
		{"empty expected rp", func(*tmproto.Attestation) {}, VerifyContext{RequestID: "r1", ExpectedRelyingPartyID: "", Now: now}, PreCheckRPMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			att := base()
			c.mut(att)
			if got := LocalPreCheck(att, c.vctx); got != c.want {
				t.Fatalf("LocalPreCheck = %q, want %q", got, c.want)
			}
		})
	}

	if got := LocalPreCheck(nil, vctx); got != PreCheckMalformed {
		t.Fatalf("nil attestation: got %q, want %q", got, PreCheckMalformed)
	}
}

func TestClaimsSatisfy(t *testing.T) {
	vi := func(claims ...tmproto.AttestationClaim) VerifiedIdentity {
		return VerifiedIdentity{Claims: NormalizeClaims(claims)}
	}
	cases := []struct {
		name     string
		have     VerifiedIdentity
		required tmproto.AttestationClaim
		want     bool
	}{
		{"higher age satisfies lower threshold", vi(tmproto.AttestationClaimAgeOver21), tmproto.AttestationClaimAgeOver18, true},
		{"exact age", vi(tmproto.AttestationClaimAgeOver18), tmproto.AttestationClaimAgeOver18, true},
		{"lower age does not satisfy higher threshold", vi(tmproto.AttestationClaimAgeOver18), tmproto.AttestationClaimAgeOver21, false},
		{"unique_human exact", vi(tmproto.AttestationClaimUniqueHuman), tmproto.AttestationClaimUniqueHuman, true},
		{"unique_human does not satisfy age", vi(tmproto.AttestationClaimUniqueHuman), tmproto.AttestationClaimAgeOver18, false},
		{"empty claim set", vi(), tmproto.AttestationClaimAgeOver18, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.have.ClaimsSatisfy(c.required); got != c.want {
				t.Fatalf("ClaimsSatisfy(%q) = %v, want %v", c.required, got, c.want)
			}
		})
	}
}

func TestNormalizeClaims(t *testing.T) {
	in := []tmproto.AttestationClaim{
		tmproto.AttestationClaimAgeOver18,
		"age_over_021",  // non-canonical: dropped
		"unique_human ", // trailing space: dropped
		tmproto.AttestationClaimUniqueHuman,
		"totally_bogus", // dropped
	}
	got := NormalizeClaims(in)
	if len(got) != 2 {
		t.Fatalf("want 2 canonical claims, got %d: %v", len(got), got)
	}
	if _, ok := got[tmproto.AttestationClaimAgeOver18]; !ok {
		t.Error("age_over_18 should survive normalization")
	}
	if _, ok := got[tmproto.AttestationClaimUniqueHuman]; !ok {
		t.Error("unique_human should survive normalization")
	}
}
