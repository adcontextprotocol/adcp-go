package tmproto

// PackageEligibility is an internal identity evaluation result.
// The targeting engine produces these; they are converted to
// EligiblePackageIDs (the spec wire format) at the boundary.
type PackageEligibility struct {
	PackageID string `json:"package_id"`
	Eligible  bool   `json:"eligible"`
}
