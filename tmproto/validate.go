package tmproto

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Geo field patterns mirrored from adcp/schemas/trusted-match/context-match-request.json.
// country is ISO 3166-1 alpha-2; region is ISO 3166-2 (e.g. "US-CA", "GB-SCT").
var (
	geoCountryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	geoRegionPattern  = regexp.MustCompile(`^[A-Z]{2}-[A-Z0-9]{1,3}$`)
)

// validateGeo checks the coarse fields the JSON Schema constrains: country
// and region patterns, and the metro sub-object's required keys. Absent
// fields are legal (all optional); a wrong type or a value that fails the
// pattern is not.
func validateGeo(geo map[string]any) error {
	if len(geo) == 0 {
		return nil
	}
	if v, present := geo["country"]; present {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("geo.country: must be string")
		}
		if !geoCountryPattern.MatchString(s) {
			return fmt.Errorf("geo.country: does not match pattern ^[A-Z]{2}$")
		}
	}
	if v, present := geo["region"]; present {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("geo.region: must be string")
		}
		if !geoRegionPattern.MatchString(s) {
			return fmt.Errorf("geo.region: does not match pattern ^[A-Z]{2}-[A-Z0-9]{1,3}$")
		}
	}
	if v, present := geo["metro"]; present {
		metro, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("geo.metro: must be object")
		}
		if _, ok := metro["system"]; !ok {
			return fmt.Errorf("geo.metro.system: required")
		}
		if _, ok := metro["value"]; !ok {
			return fmt.Errorf("geo.metro.value: required")
		}
	}
	return nil
}

// Message type discriminators required on every TMP envelope. The spec
// instructs agents to reject requests whose `type` does not match the
// endpoint, and to stamp the corresponding response type on outbound
// payloads.
const (
	TypeContextMatchRequest   = "context_match_request"
	TypeContextMatchResponse  = "context_match_response"
	TypeIdentityMatchRequest  = "identity_match_request"
	TypeIdentityMatchResponse = "identity_match_response"
	TypeError                 = "error"
)

// Maximum sizes for request arrays to prevent denial-of-service.
const (
	MaxPackagesPerRequest     = 500
	MaxArtifactRefsPerRequest = 20
	// MaxIdentitiesPerRequest mirrors the TMP schema's maxItems on identities
	// — matches the TMPX plaintext budget (~120 bytes after HPKE overhead).
	MaxIdentitiesPerRequest = 3
)

// Bounds on the experimental verified-identity attestation surface
// (trusted_match.verified_identity). Receivers MUST bound attestation and
// sealed-credential count and size to prevent DoS amplification; these mirror
// the maxItems / maxLength constraints in the TMP schema.
const (
	MaxSealedCredentials       = 8
	MaxSealedCredentialPayload = 8192
	MaxAudienceKIDLength       = 128
	MaxAttestationClaims       = 16
)

// validUIDTypes is the closed set of identifier types the TMP schema allows
// on identity-match identities. Drawn from /schemas/enums/uid-type.json.
var validUIDTypes = map[UIDType]struct{}{
	UIDTypeRampID:              {},
	UIDTypeRampIDDerived:       {},
	UIDTypeID5:                 {},
	UIDTypeUID2:                {},
	UIDTypeEUID:                {},
	UIDTypePairID:              {},
	UIDTypeMAID:                {},
	UIDTypeHashedEmail:         {},
	UIDTypePublisherFirstParty: {},
	UIDTypeWorldIDNullifier:    {},
	UIDTypeOther:               {},
}

// MaxIDLength caps identifier fields to prevent oversized store keys.
const MaxIDLength = 256

// validateSafeID checks that an ID does not contain characters that
// could cause Store key injection (`:`, `/`, `\`) or terminal / log
// injection (any C0 control 0x00–0x1F or DEL 0x7F: NUL, BEL, BS, TAB,
// LF, VT, FF, CR, ESC, ...), and is within length limits. The 7-bit
// ESC (0x1B) is the introducer of ANSI escape sequences such as
// "\x1B[2J" (clear screen), so catching ESC blocks the relevant
// terminal-injection vector even though the strict C1 CSI byte
// (0x9B) is not in the C0 range. Used on every wire-supplied
// identifier the agent persists, echoes in logs, or routes through
// SafeRequestIDForEcho.
func validateSafeID(field, value string) error {
	if len(value) > MaxIDLength {
		return fmt.Errorf("%s exceeds maximum length of %d", field, MaxIDLength)
	}
	if strings.ContainsAny(value, ":/\\") {
		return fmt.Errorf("%s contains invalid characters", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7F {
			return fmt.Errorf("%s contains invalid characters", field)
		}
	}
	return nil
}

// MaxSellerAgentURLLength caps the seller_agent_url string. The value flows
// into structured logs, cache keys, and the RFC 9421 @target-uri signing
// input; a pathologically long URL bloats every one of those.
const MaxSellerAgentURLLength = 2048

// validateSellerAgentURL rejects a seller_agent_url that carries C0 control
// bytes (0x00–0x1F) or DEL (0x7F). The value is copied into the router's
// context cache key (which uses NUL as a separator), echoed in structured
// logs, and folded into the RFC 9421 @target-uri signing input — rejecting
// control bytes at the wire boundary keeps all three surfaces well-formed.
// Callers still check non-emptiness themselves so the error message can
// name the request shape ("required" vs "invalid").
func validateSellerAgentURL(value string) error {
	if len(value) > MaxSellerAgentURLLength {
		return fmt.Errorf("seller_agent_url exceeds maximum length of %d", MaxSellerAgentURLLength)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7F {
			return errors.New("seller_agent_url contains invalid characters")
		}
	}
	return nil
}

// SafeRequestIDForEcho returns requestID only when it satisfies the same
// constraints as request_id validation. HTTP boundaries use it before echoing a
// parsed request_id in an error response or structured log.
func SafeRequestIDForEcho(requestID string) string {
	if requestID == "" {
		return ""
	}
	if err := validateSafeID("request_id", requestID); err != nil {
		return ""
	}
	return requestID
}

func validateProtocolVersion(v string) error {
	if v == "" || v == "1.0" {
		return nil
	}
	return errors.New("unsupported protocol_version")
}

// ValidateContextRequest checks that required fields are present on a
// context match request and that the message-type discriminator matches the
// endpoint per the TMP spec: a mismatched or missing `type` must be
// rejected.
func ValidateContextRequest(req *ContextMatchRequest) error {
	if req.Type != TypeContextMatchRequest {
		return fmt.Errorf("type must be %q", TypeContextMatchRequest)
	}
	if err := validateProtocolVersion(req.ProtocolVersion); err != nil {
		return err
	}
	if err := validateSafeID("request_id", req.RequestID); err != nil {
		return err
	}
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if req.PropertyRID == "" {
		return errors.New("property_rid is required")
	}
	if err := validateSafeID("property_rid", req.PropertyRID); err != nil {
		return err
	}
	if err := validateSafeID("property_id", req.PropertyID); err != nil {
		return err
	}
	if req.PropertyType == "" {
		return errors.New("property_type is required")
	}
	if req.PlacementID == "" {
		return errors.New("placement_id is required")
	}
	if err := validateSafeID("placement_id", req.PlacementID); err != nil {
		return err
	}
	if req.SellerAgentURL == "" {
		return errors.New("seller_agent_url is required")
	}
	if err := validateSellerAgentURL(req.SellerAgentURL); err != nil {
		return err
	}
	if len(req.PackageIDs) > MaxPackagesPerRequest {
		return fmt.Errorf("package_ids exceeds maximum of %d", MaxPackagesPerRequest)
	}
	if len(req.ArtifactRefs) > MaxArtifactRefsPerRequest {
		return fmt.Errorf("artifact_refs exceeds maximum of %d", MaxArtifactRefsPerRequest)
	}
	for _, id := range req.PackageIDs {
		if err := validateSafeID("package_id", id); err != nil {
			return err
		}
	}
	// Ladder-level schema constraints: the JSON Schema caps sizes and
	// enumerates enums; those are enforced here so a malformed request is
	// rejected at handler entry instead of surviving into the engine
	// (where an out-of-range sentiment or a 60-topic list would silently
	// mismatch every taxonomy check).
	if err := req.ContextSignals.Validate(); err != nil {
		return err
	}
	for i, ref := range req.ArtifactRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("artifact_refs[%d]: %w", i, err)
		}
	}
	if err := req.Artifact.Validate(); err != nil {
		return err
	}
	if err := validateGeo(req.Geo); err != nil {
		return err
	}
	return nil
}

// ValidateIdentityRequest checks that required fields are present on an
// identity match request and that the message-type discriminator matches the
// endpoint per the TMP spec: a mismatched or missing `type` must be rejected.
func ValidateIdentityRequest(req *IdentityMatchRequest) error {
	if req.Type != TypeIdentityMatchRequest {
		return fmt.Errorf("type must be %q", TypeIdentityMatchRequest)
	}
	if err := validateProtocolVersion(req.ProtocolVersion); err != nil {
		return err
	}
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if err := validateSafeID("request_id", req.RequestID); err != nil {
		return err
	}
	if req.SellerAgentURL == "" {
		return errors.New("seller_agent_url is required")
	}
	if err := validateSellerAgentURL(req.SellerAgentURL); err != nil {
		return err
	}
	if len(req.Identities) == 0 {
		return errors.New("identities must not be empty")
	}
	if len(req.Identities) > MaxIdentitiesPerRequest {
		return fmt.Errorf("identities exceeds maximum of %d", MaxIdentitiesPerRequest)
	}
	// Duplicate (uid_type, user_token) pairs MUST NOT appear. Rejecting them
	// keeps the signed identities set byte-aligned with the forwarded set: the
	// signing hash collapses duplicates, so allowing two entries with the same
	// (uid_type, user_token) but different attestation would let the second
	// entry's attestation ride along unsigned.
	type identityKey struct{ uid, token string }
	seen := make(map[identityKey]struct{}, len(req.Identities))
	for i, id := range req.Identities {
		if id.UserToken == "" {
			return fmt.Errorf("identities[%d].user_token is required", i)
		}
		if len(id.UserToken) > MaxIDLength {
			return fmt.Errorf("identities[%d].user_token exceeds %d bytes", i, MaxIDLength)
		}
		if id.UIDType == "" {
			return fmt.Errorf("identities[%d].uid_type is required", i)
		}
		if _, ok := validUIDTypes[id.UIDType]; !ok {
			return fmt.Errorf("identities[%d].uid_type is not a recognized TMP identity type", i)
		}
		k := identityKey{string(id.UIDType), id.UserToken}
		if _, dup := seen[k]; dup {
			return fmt.Errorf("identities[%d] duplicates an earlier (uid_type, user_token) pair", i)
		}
		seen[k] = struct{}{}
		if id.Attestation != nil {
			if err := validateAttestation(i, id.Attestation); err != nil {
				return err
			}
		}
	}
	if req.Country != "" {
		req.Country = strings.ToUpper(req.Country)
		if len(req.Country) != 2 || req.Country[0] < 'A' || req.Country[0] > 'Z' || req.Country[1] < 'A' || req.Country[1] > 'Z' {
			return errors.New("country must be a 2-letter ISO 3166-1 alpha-2 code")
		}
	}
	if len(req.PackageIDs) > MaxPackagesPerRequest {
		return fmt.Errorf("package_ids exceeds maximum of %d", MaxPackagesPerRequest)
	}
	for _, id := range req.PackageIDs {
		if err := validateSafeID("package_id", id); err != nil {
			return err
		}
	}
	if err := validateConsent(req.Consent); err != nil {
		return err
	}
	if len(req.SealedCredentials) > MaxSealedCredentials {
		return fmt.Errorf("sealed_credentials exceeds maximum of %d", MaxSealedCredentials)
	}
	for i, sc := range req.SealedCredentials {
		if sc.AudienceKID == "" {
			return fmt.Errorf("sealed_credentials[%d].audience_kid is required", i)
		}
		if len(sc.AudienceKID) > MaxAudienceKIDLength {
			return fmt.Errorf("sealed_credentials[%d].audience_kid exceeds %d bytes", i, MaxAudienceKIDLength)
		}
		if sc.Payload == "" {
			return fmt.Errorf("sealed_credentials[%d].payload is required", i)
		}
		if len(sc.Payload) > MaxSealedCredentialPayload {
			return fmt.Errorf("sealed_credentials[%d].payload exceeds %d bytes", i, MaxSealedCredentialPayload)
		}
	}
	return nil
}

// validateConsent enforces the cross-field consent rule the schema encodes
// in prose: the identity-match spec says "buyers in regulated jurisdictions
// MUST NOT process the user token without consent information." We can't
// infer jurisdiction from the request, but the caller declares it via
// `consent.gdpr == true`. When that flag is set, one of `tcf_consent` or
// `gpp` must accompany it or the request is rejected — processing an
// unconsented GDPR request is the non-compliance the spec targets.
//
// Absent consent object is allowed (non-regulated request, or an operator
// deploying outside the EU); the caller has told us jurisdiction does not
// apply.
func validateConsent(consent map[string]any) error {
	if len(consent) == 0 {
		return nil
	}
	gdprAny, present := consent["gdpr"]
	if !present {
		return nil
	}
	gdpr, ok := gdprAny.(bool)
	if !ok {
		return fmt.Errorf("consent.gdpr: must be boolean")
	}
	if !gdpr {
		return nil
	}
	if hasNonEmptyString(consent, "tcf_consent") || hasNonEmptyString(consent, "gpp") {
		return nil
	}
	return errors.New("consent.gdpr is true but neither tcf_consent nor gpp is present; refusing to process user tokens without consent")
}

func hasNonEmptyString(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	return ok && s != ""
}

// validateAttestation checks the structural and DoS-bound constraints on a
// per-identity attestation. It deliberately does NOT verify the proof, the
// signal binding, relying-party provenance, or expiry — those are the
// receiving buyer's job (see docs/trusted-match/specification.mdx §Verified
// Identity Attestation conformance). Nor does it reject unrecognized claim
// values: the claim set is additive, so an older receiver must tolerate a
// newer threshold rather than rejecting the whole request.
func validateAttestation(i int, a *Attestation) error {
	if len(a.Issuer) == 0 {
		return fmt.Errorf("identities[%d].attestation.issuer is required", i)
	}
	if a.Scheme == "" {
		return fmt.Errorf("identities[%d].attestation.scheme is required", i)
	}
	if len(a.Proof) == 0 {
		return fmt.Errorf("identities[%d].attestation.proof is required", i)
	}
	if len(a.Claims) == 0 {
		return fmt.Errorf("identities[%d].attestation.claims must not be empty", i)
	}
	if len(a.Claims) > MaxAttestationClaims {
		return fmt.Errorf("identities[%d].attestation.claims exceeds maximum of %d", i, MaxAttestationClaims)
	}
	seen := make(map[AttestationClaim]struct{}, len(a.Claims))
	for _, c := range a.Claims {
		if _, dup := seen[c]; dup {
			return fmt.Errorf("identities[%d].attestation.claims contains duplicate %q", i, c)
		}
		seen[c] = struct{}{}
	}
	return nil
}
