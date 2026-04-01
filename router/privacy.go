package router

import (
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// ValidateContextRequest ensures required fields are present on a context match request.
func ValidateContextRequest(req *tmproto.ContextMatchRequest) error {
	return tmproto.ValidateContextRequest(req)
}

// ValidateIdentityRequest ensures required fields are present on an identity match request.
func ValidateIdentityRequest(req *tmproto.IdentityMatchRequest) error {
	return tmproto.ValidateIdentityRequest(req)
}
