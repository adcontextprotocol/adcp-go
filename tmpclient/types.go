package tmpclient

import (
	"fmt"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TMPError is a structured error from a TMP endpoint.
type TMPError struct {
	StatusCode int
	Code       tmproto.ErrorCode
	Message    string
	RequestID  string
}

func (e *TMPError) Error() string {
	return fmt.Sprintf("tmp %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// ActivateParams configures the full activation flow.
// Privacy is enforced by API design: context fields and identity fields
// are separated and cannot be mixed.
type ActivateParams struct {
	// Context match inputs.
	PropertyID   string
	PropertyType tmproto.PropertyType
	PlacementID  string
	Artifacts    []string
	Geo          *tmproto.Geo
	Packages     []tmproto.AvailablePackage

	// Identity match inputs.
	UserToken  string
	UIDType    tmproto.UIDType
	PackageIDs []string // ALL buyer packages (may be superset of Packages)
	Consent    *tmproto.ConsentSignals
}

// Activation is a package that passed both context and identity checks.
type Activation struct {
	PackageID   string
	MediaBuyID  string
	Offer       tmproto.Offer
	IntentScore *float64
}

// ActivateResult is the joined output of parallel context + identity calls.
type ActivateResult struct {
	Activations []Activation
	Signals     *tmproto.Signals
	Context     *tmproto.ContextMatchResponse
	Identity    *tmproto.IdentityMatchResponse
}
