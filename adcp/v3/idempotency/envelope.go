package idempotency

import (
	"encoding/json"
	"fmt"
)

// InjectReplayed sets {"replayed": replayed} on an AdCP response envelope.
//
// Store.Wrap caches and returns the inner handler response; envelope
// construction (adcp_version, message, status, replayed) happens one layer
// above, in the transport adapter. Adapters MUST set `replayed` on the
// envelope — the compliance storyboard validates this field for cached
// responses — and this helper is the spec-compliant way to do it.
//
// If the envelope is not a JSON object, an error is returned.
func InjectReplayed(envelope []byte, replayed bool) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(envelope, &m); err != nil {
		return nil, fmt.Errorf("idempotency: decode envelope: %w", err)
	}
	m["replayed"] = replayed
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("idempotency: encode envelope: %w", err)
	}
	return out, nil
}
