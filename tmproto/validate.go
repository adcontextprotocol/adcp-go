package tmproto

import (
	"errors"
	"fmt"
	"strings"
)

// Maximum sizes for request arrays to prevent denial-of-service.
const (
	MaxPackagesPerRequest   = 500
	MaxArtifactRefsPerRequest = 20
)

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

func validateProtocolVersion(v string) error {
	if v == "" || v == "1.0" {
		return nil
	}
	return errors.New("unsupported protocol_version")
}

// ValidateContextRequest checks that required fields are present on a context match request.
func ValidateContextRequest(req *ContextMatchRequest) error {
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

// ValidateIdentityRequest checks that required fields are present on an identity match request.
func ValidateIdentityRequest(req *IdentityMatchRequest) error {
	if err := validateProtocolVersion(req.ProtocolVersion); err != nil {
		return err
	}
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if err := validateSafeID("request_id", req.RequestID); err != nil {
		return err
	}
	if req.UserToken == "" {
		return errors.New("user_token is required")
	}
	if req.UIDType == "" {
		return errors.New("uid_type is required")
	}
	if req.Country != "" {
		req.Country = strings.ToUpper(req.Country)
		if len(req.Country) != 2 || req.Country[0] < 'A' || req.Country[0] > 'Z' || req.Country[1] < 'A' || req.Country[1] > 'Z' {
			return errors.New("country must be a 2-letter ISO 3166-1 alpha-2 code")
		}
	}
	if len(req.PackageIDs) == 0 {
		return errors.New("package_ids must not be empty")
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

