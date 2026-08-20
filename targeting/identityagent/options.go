package identityagent

// EUIDOperator is the interface the identityagent constructors accept for
// decoding EUID advertising tokens. It is structurally identical to
// UID2Operator — the two wire formats share the AEAD envelope and differ
// only by operator URL and identity-scope bit — but the alias keeps
// UID2/EUID plumbing distinguishable at call sites and leaves room for
// EUID-specific behavior to be added later without breaking callers.
type EUIDOperator = UID2Operator

// Option configures a TMPXSealer or IdentityCanonicalizer built via the
// *WithOptions constructors. Options are additive: introducing a new
// sidecar (or a new field on an existing one) is done by adding a WithX
// helper here rather than by growing constructor signatures, which would
// break source-compat for external callers.
type Option func(*options)

// options is the accumulated sidecar surface applied by *WithOptions.
// Each field may be nil; the constructors interpret nil the same way
// they interpret "the operator did not configure this UID type" — a
// silent drop at decode time for the affected UID types.
type options struct {
	liveRampSidecar LiveRampSidecar
	uid2Operator    UID2Operator
	euidOperator    EUIDOperator
}

// WithLiveRampSidecar wires the LiveRamp sidecar client used to decode
// RampID and RampID-derived identities. Pass a nil interface — not a
// typed-nil pointer to a concrete client — to leave RampID decoding
// disabled (Go's interface-nil rules treat a typed nil as non-nil).
func WithLiveRampSidecar(client LiveRampSidecar) Option {
	return func(o *options) { o.liveRampSidecar = client }
}

// WithUID2Operator wires the UID2 operator client used to decode UID2
// advertising tokens. Pass a nil interface to leave UID2 decoding
// disabled.
func WithUID2Operator(client UID2Operator) Option {
	return func(o *options) { o.uid2Operator = client }
}

// WithEUIDOperator wires the EUID operator client used to decode EUID
// advertising tokens. UID2 and EUID are scope-bound (each operator client
// is initialized with its own scope), so callers pass either or both
// independently. Pass a nil interface to leave EUID decoding disabled.
func WithEUIDOperator(client EUIDOperator) Option {
	return func(o *options) { o.euidOperator = client }
}

// applyOptions materializes an options value from the supplied Option
// list. Extracted so both *WithOptions constructors apply options the
// same way without duplicating the loop.
func applyOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}
