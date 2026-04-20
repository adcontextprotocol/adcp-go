package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SubscriberConfig is the typed form of push_notification_config. Publishers
// decode this from the originating AdCP request to find where to deliver
// webhook events.
//
// The Authentication field is the legacy HMAC-SHA256 / Bearer path from
// AdCP 3.x (deprecated, removed in 4.0). Publishers SHOULD ignore it and use
// webhook-signing (RFC 9421) regardless; the field is exposed so callers
// implementing the legacy fallback can opt in explicitly.
type SubscriberConfig struct {
	// URL is the subscriber's webhook endpoint. Required.
	URL string `json:"url"`

	// Token is a client-provided validation token that some receivers echo
	// back in the webhook payload so clients can correlate deliveries
	// without parsing URL paths. Not a security mechanism.
	Token string `json:"token,omitempty"`

	// Authentication is the optional legacy auth config. Present on AdCP 3.x
	// publishers that opted into HMAC-SHA256 or Bearer before the
	// webhook-signing baseline. nil on publishers using 9421 exclusively.
	Authentication *SubscriberAuthentication `json:"authentication,omitempty"`
}

// SubscriberAuthentication is the A2A-shape authentication block on
// push_notification_config. Schemes is a single-entry array per the schema
// (minItems=1, maxItems=1).
type SubscriberAuthentication struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials"`
}

// ErrMissingURL is returned by DecodeConfig when the decoded config has no
// url field. Receivers with no URL are not subscribers — publishers should
// skip them rather than error.
var ErrMissingURL = errors.New("webhook: push_notification_config missing url")

// DecodeConfig decodes the any-typed push_notification_config field on an
// AdCP request into a typed SubscriberConfig. The generated request types
// (CreateMediaBuyRequest, SyncAccountsRequest, etc.) carry this field as
// `any` because the field composes multiple schemas; DecodeConfig handles
// the re-marshal + decode round trip.
//
// Returns (nil, nil) when raw is nil — "no subscriber configured" is a
// normal state, not an error. Returns (nil, ErrMissingURL) when raw is
// present but has no url.
func DecodeConfig(raw any) (*SubscriberConfig, error) {
	if raw == nil {
		return nil, nil
	}
	// Re-marshal through JSON. raw is either map[string]any (from a decode
	// of unknown shape) or a typed struct with json tags; Marshal handles
	// both uniformly.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("webhook: marshal push_notification_config: %w", err)
	}
	// An empty JSON object at this layer is equivalent to nil.
	if string(b) == "null" || string(b) == "{}" {
		return nil, nil
	}
	var cfg SubscriberConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("webhook: decode push_notification_config: %w", err)
	}
	if cfg.URL == "" {
		return nil, ErrMissingURL
	}
	return &cfg, nil
}
