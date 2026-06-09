package tmproto

// IdentityToken carries one opaque user identifier and the type of identity
// graph it came from. Publishers include one entry per token they have; the
// buyer resolves on whichever graph matches. Used by IdentityMatchRequest.
type IdentityToken struct {
	UserToken   string       `json:"user_token"`
	UIDType     UIDType      `json:"uid_type"`
	Attestation *Attestation `json:"attestation,omitempty"`
}

// AttestationClaim is a claim a verified-identity attestation establishes about
// a user — proof of personhood or an age threshold. The set is closed and
// issuer-agnostic; see the attestation-claim enum in the TMP schema. New
// thresholds are added additively, so receivers MUST tolerate values they do
// not recognize rather than rejecting them.
type AttestationClaim string

const (
	AttestationClaimUniqueHuman AttestationClaim = "unique_human"
	AttestationClaimAgeOver13   AttestationClaim = "age_over_13"
	AttestationClaimAgeOver16   AttestationClaim = "age_over_16"
	AttestationClaimAgeOver18   AttestationClaim = "age_over_18"
	AttestationClaimAgeOver21   AttestationClaim = "age_over_21"
)

// Attestation is verifiable proof ABOUT an identity (proof-of-personhood and/or
// age) carried on an IdentityMatchRequest identity entry. The receiver verifies
// it cryptographically rather than trusting an assertion, and MUST treat an
// unverifiable attestation as absent — never as an asserted-true claim.
// Issuer-agnostic: World ID is the first scheme, with mDL / VC-style issuers
// using the same shape. See docs/trusted-match/specification.mdx §Verified
// Identity Attestation.
//
// Issuer and Proof are held as decoded JSON rather than concrete Go types so
// they round-trip byte-for-byte through the request signature: Issuer is an
// open-ended brand reference and Proof is scheme-defined opaque material, and
// re-encoding either through a narrower type could drop fields the signer
// covered, breaking verification.
//
// Because the attestation is folded into the signed canonical bytes, any JSON
// numbers inside Issuer or Proof are canonicalized via RFC 8785 JCS, which this
// package implements for integers only (see jcs.go). World ID and comparable
// schemes carry their proof material as strings, so this holds in practice; a
// scheme that puts a non-integer or larger-than-int64 number in Proof would
// fail signing closed (a clean error, never a wrong signature) until JCS gains
// general number canonicalization.
type Attestation struct {
	Issuer            map[string]any     `json:"issuer"`
	Scheme            string             `json:"scheme"`
	RelyingPartyID    string             `json:"relying_party_id,omitempty"`
	Action            string             `json:"action,omitempty"`
	Claims            []AttestationClaim `json:"claims"`
	VerificationLevel string             `json:"verification_level,omitempty"`
	SignalBinding     string             `json:"signal_binding,omitempty"`
	Proof             map[string]any     `json:"proof"`
	ExpiresAt         string             `json:"expires_at,omitempty"`
}

// SealedCredential is an HPKE-sealed verified-identity credential addressed to a
// specific audience — the network-as-RP carrier on an IdentityMatchRequest. The
// publisher relays Payload untouched and cannot open it; only the audience that
// owns AudienceKID can decrypt the inner attestation. Payload reuses the TMPX
// envelope format `kid.base64url_nopad(ciphertext)`.
type SealedCredential struct {
	AudienceKID string `json:"audience_kid"`
	Payload     string `json:"payload"`
}
