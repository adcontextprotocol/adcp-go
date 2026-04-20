package webhook

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeConfigNilReturnsNil(t *testing.T) {
	cfg, err := DecodeConfig(nil)
	require.NoError(t, err)
	assert.Nil(t, cfg, "nil push_notification_config is a no-op, not an error")
}

func TestDecodeConfigEmptyObjectReturnsNil(t *testing.T) {
	cfg, err := DecodeConfig(map[string]any{})
	require.NoError(t, err)
	assert.Nil(t, cfg, "empty object means no subscriber configured")
}

func TestDecodeConfigHappyPath(t *testing.T) {
	raw := map[string]any{
		"url":   "https://buyer.example.com/webhooks/task-status",
		"token": "opaque-client-validation-token-1234567890",
	}
	cfg, err := DecodeConfig(raw)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "https://buyer.example.com/webhooks/task-status", cfg.URL)
	assert.Equal(t, "opaque-client-validation-token-1234567890", cfg.Token)
	assert.Nil(t, cfg.Authentication, "webhook-signing default: no legacy auth")
}

func TestDecodeConfigWithLegacyAuth(t *testing.T) {
	raw := map[string]any{
		"url": "https://buyer.example.com/webhooks/task-status",
		"authentication": map[string]any{
			"schemes":     []any{"HMAC-SHA256"},
			"credentials": "shared-secret-exchanged-out-of-band-12345678",
		},
	}
	cfg, err := DecodeConfig(raw)
	require.NoError(t, err)
	require.NotNil(t, cfg.Authentication)
	assert.Equal(t, []string{"HMAC-SHA256"}, cfg.Authentication.Schemes)
	assert.Equal(t, "shared-secret-exchanged-out-of-band-12345678", cfg.Authentication.Credentials)
}

func TestDecodeConfigMissingURL(t *testing.T) {
	raw := map[string]any{"token": "abc"}
	_, err := DecodeConfig(raw)
	assert.ErrorIs(t, err, ErrMissingURL)
}

func TestDecodeConfigFromTypedStruct(t *testing.T) {
	// Common call site: the request's PushNotificationConfig field is typed
	// `any` in types_gen.go; decoders may populate it from JSON as a
	// map[string]any (above) or as a user-defined struct. Both must work.
	type myConfig struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	cfg, err := DecodeConfig(myConfig{URL: "https://x.example.com/hook", Token: "tok-long-enough-to-matter"})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "https://x.example.com/hook", cfg.URL)
}

func TestDecodeConfigInvalidJSON(t *testing.T) {
	// math.NaN round-trips as a value json.Marshal refuses.
	type badConfig struct {
		URL string  `json:"url"`
		Bad float64 `json:"bad"`
	}
	// Populate Bad with NaN so json.Marshal errors.
	nan := float64(0)
	nan = nan / nan // NaN without importing math
	_, err := DecodeConfig(badConfig{URL: "https://x", Bad: nan})
	require.Error(t, err)
}
