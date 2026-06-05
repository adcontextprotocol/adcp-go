package contextagent

import "github.com/adcontextprotocol/adcp-go/targeting"

// Option customizes a Run invocation. Options carry dependencies the
// agent cannot construct from env alone — most commonly an externally
// hydrated property bitmap fed by a registry sync loop in the binary
// entrypoint, instead of the PROPERTY_RIDS env fallback.
type Option func(*runOptions)

type runOptions struct {
	propertyGlobal  targeting.Bitmap
	livenessChecks  []LivenessCheck
}

// WithPropertyGlobal injects the global property bitmap the engine
// consults at the top of every request. When supplied, the bitmap
// replaces the one derived from Config.PropertyRIDs. Use this to feed
// the agent from a registry.PropertyIndex that is kept fresh by a
// background syncer; the bitmap is read on every request, so a dynamic
// implementation gets eventual-consistency for free.
func WithPropertyGlobal(b targeting.Bitmap) Option {
	return func(o *runOptions) {
		o.propertyGlobal = b
	}
}

// WithLivenessChecks appends additional /live predicates to the
// default suppression-snapshot check. Use this to surface the health
// of binary-owned dependencies (e.g. a registry sync loop) into the
// same /live response the rest of the agent reports through.
func WithLivenessChecks(checks ...LivenessCheck) Option {
	return func(o *runOptions) {
		o.livenessChecks = append(o.livenessChecks, checks...)
	}
}

func applyOptions(opts []Option) runOptions {
	var ro runOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&ro)
		}
	}
	return ro
}
