package identityagent

import "github.com/adcontextprotocol/adcp-go/targeting"

// RunOption injects optional dependencies into Run that cannot come from
// environment configuration because they are constructed objects, not strings —
// chiefly the verified-identity verifier and its HPKE recipient keys. These are
// issuer-specific (e.g. World ID, which carries an HTTP client to World's
// backend), so they are kept out of this core package's dependency graph: the
// deployable binary constructs them and passes them in, and this package only
// ever sees the targeting.AttestationVerifier / targeting.AgeResolver
// interfaces and the RecipientKey data.
//
// With no options, Run behaves exactly as before: no verifier ⇒ every
// attestation is treated as absent (fail-closed), and no age gating.
type RunOption func(*runOptions)

type runOptions struct {
	verifier       targeting.AttestationVerifier
	recipientKeys  map[string]RecipientKey
	ageResolver    targeting.AgeResolver
	relyingPartyID string
}

// WithAttestationVerifier injects the verified-identity verifier. Without it,
// attestations and sealed credentials are treated as absent and eligibility is
// unchanged — trust-without-verify is unreachable by omission.
func WithAttestationVerifier(v targeting.AttestationVerifier) RunOption {
	return func(o *runOptions) { o.verifier = v }
}

// WithRecipientKeys injects the HPKE recipient keys (audience_kid → key + the
// relying party this deployment acts as) used to open sealed_credentials.
func WithRecipientKeys(keys map[string]RecipientKey) RunOption {
	return func(o *runOptions) { o.recipientKeys = keys }
}

// WithRelyingPartyID sets the relying party this deployment acts as for in-band
// attestation verification (req.Identities[].Attestation). Without it, only the
// sealed_credentials carrier is active; in-band attestations are treated as
// absent.
func WithRelyingPartyID(rpID string) RunOption {
	return func(o *runOptions) { o.relyingPartyID = rpID }
}

// WithAgeResolver injects the age-policy resolver (e.g. AdCP Policy Registry
// backed). Without it, no age gating is applied.
func WithAgeResolver(r targeting.AgeResolver) RunOption {
	return func(o *runOptions) { o.ageResolver = r }
}
