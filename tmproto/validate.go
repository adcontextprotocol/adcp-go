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

// validateSafeID checks that an ID does not contain characters that could
// cause Store key injection (colons, slashes, newlines).
func validateSafeID(field, value string) error {
	if strings.ContainsAny(value, ":\n\r\t/\\") {
		return fmt.Errorf("%s contains invalid characters", field)
	}
	return nil
}

func validateProtocolVersion(v string) error {
	if v == "" || v == "1.0" {
		return nil
	}
	return fmt.Errorf("unsupported protocol_version: %s", v)
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
	if req.UserToken == "" && len(req.Identities) == 0 {
		return errors.New("user_token or identities is required")
	}
	if len(req.PackageIDs) == 0 {
		return errors.New("package_ids must not be empty")
	}
	if len(req.PackageIDs) > MaxPackagesPerRequest {
		return fmt.Errorf("package_ids exceeds maximum of %d", MaxPackagesPerRequest)
	}
	if len(req.Identities) > MaxIdentitiesPerRequest {
		return fmt.Errorf("identities exceeds maximum of %d", MaxIdentitiesPerRequest)
	}
	for _, id := range req.PackageIDs {
		if err := validateSafeID("package_id", id); err != nil {
			return err
		}
	}
	return nil
}
