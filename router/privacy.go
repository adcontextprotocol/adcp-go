package main

import (
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// ValidateContextRequest ensures no identity fields leaked into a context request.
func ValidateContextRequest(req *tmp.ContextMatchRequest) error {
	if req.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if req.PropertyID == "" {
		return fmt.Errorf("property_id is required")
	}
	if req.PlacementID == "" {
		return fmt.Errorf("placement_id is required")
	}
	if len(req.AvailablePkgs) == 0 {
		return fmt.Errorf("available_packages must not be empty")
	}
	return nil
}

// ValidateIdentityRequest ensures no context fields leaked into an identity request.
func ValidateIdentityRequest(req *tmp.IdentityMatchRequest) error {
	if req.RequestID == "" {
		return fmt.Errorf("request_id is required")
	}
	if req.UserToken == "" {
		return fmt.Errorf("user_token is required")
	}
	if len(req.PackageIDs) == 0 {
		return fmt.Errorf("package_ids must not be empty")
	}
	return nil
}
