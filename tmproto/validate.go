package tmproto

import "errors"

// ValidateContextRequest checks that required fields are present on a context match request.
func ValidateContextRequest(req *ContextMatchRequest) error {
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if req.PropertyID == "" {
		return errors.New("property_id is required")
	}
	if req.PlacementID == "" {
		return errors.New("placement_id is required")
	}
	if len(req.AvailablePkgs) == 0 {
		return errors.New("available_packages must not be empty")
	}
	return nil
}

// ValidateIdentityRequest checks that required fields are present on an identity match request.
func ValidateIdentityRequest(req *IdentityMatchRequest) error {
	if req.RequestID == "" {
		return errors.New("request_id is required")
	}
	if req.UserToken == "" {
		return errors.New("user_token is required")
	}
	if len(req.PackageIDs) == 0 {
		return errors.New("package_ids must not be empty")
	}
	return nil
}
