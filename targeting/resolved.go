package targeting

// ResolvedPackages carries the per-package identity-side configuration
// the identity agent consults to evaluate eligibility. It is constructed
// by the identity agent's bundle from the identityconfig snapshot —
// identity-match keeps this shape stable, and the context engine no
// longer touches it.
type ResolvedPackages struct {
	IdentityConfigs map[string]*PackageIdentityConfig // pkgID → config
}
