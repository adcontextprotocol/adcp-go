package tmproto

import (
	"errors"
	"fmt"
	"strings"
)

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
	UIDTypeOther:               {},
}

// MaxIDLength caps identifier fields to prevent oversized store keys.
const MaxIDLength = 256

// validateSafeID checks that an ID does not contain characters that could
// cause Store key injection (colons, slashes, newlines) and is within length limits.
func validateSafeID(field, value string) error {
	if len(value) > MaxIDLength {
		return fmt.Errorf("%s exceeds maximum length of %d", field, MaxIDLength)
	}
	if strings.ContainsAny(value, ":\n\r\t/\\") {
		return fmt.Errorf("%s contains invalid characters", field)
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
	if req.PropertyID == "" {
		return errors.New("property_id is required")
	}
	if err := validateSafeID("property_id", req.PropertyID); err != nil {
		return err
	}
	if err := validateSafeID("property_rid", req.PropertyRID); err != nil {
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
	if len(req.Identities) == 0 {
		return errors.New("identities must not be empty")
	}
	if len(req.Identities) > MaxIdentitiesPerRequest {
		return fmt.Errorf("identities exceeds maximum of %d", MaxIdentitiesPerRequest)
	}
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
	return nil
}
