package tmproto

import (
	"errors"
	"fmt"
	"strings"
)

// Maximum sizes for request arrays to prevent denial-of-service.
const (
	MaxPackagesPerRequest = 500
	MaxArtifactsPerRequest = 50
	MaxIdentitiesPerRequest = 10
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
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if req.PropertyID == "" {
		return errors.New("property_id is required")
	}
	if req.PropertyType == "" {
		return errors.New("property_type is required")
	}
	if req.PlacementID == "" {
		return errors.New("placement_id is required")
	}
	if len(req.AvailablePkgs) == 0 {
		return errors.New("available_packages must not be empty")
	}
	if len(req.AvailablePkgs) > MaxPackagesPerRequest {
		return fmt.Errorf("available_packages exceeds maximum of %d", MaxPackagesPerRequest)
	}
	if len(req.Artifacts) > MaxArtifactsPerRequest {
		return fmt.Errorf("artifacts exceeds maximum of %d", MaxArtifactsPerRequest)
	}
	for _, pkg := range req.AvailablePkgs {
		if err := validateSafeID("package_id", pkg.PackageID); err != nil {
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
	if req.UserToken == "" {
		return errors.New("user_token is required")
	}
	if req.UIDType == "" {
		return errors.New("uid_type is required")
	}
	if req.Country != "" && len(req.Country) != 2 {
		return errors.New("country must be a 2-letter ISO 3166-1 alpha-2 code")
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

// ValidateExposeRequest checks that required fields are present on an expose request.
func ValidateExposeRequest(req *ExposeRequest) error {
	if req.UserToken == "" && len(req.Identities) == 0 {
		return errors.New("user_token or identities is required")
	}
	if req.PackageID == "" {
		return errors.New("package_id is required")
	}
	if err := validateSafeID("package_id", req.PackageID); err != nil {
		return err
	}
	if req.SourceID != "" {
		if err := validateSafeID("source_id", req.SourceID); err != nil {
			return err
		}
	}
	if req.ImpressionID != "" {
		if err := validateSafeID("impression_id", req.ImpressionID); err != nil {
			return err
		}
	}
	if req.CampaignID != "" {
		if err := validateSafeID("campaign_id", req.CampaignID); err != nil {
			return err
		}
	}
	return nil
}
