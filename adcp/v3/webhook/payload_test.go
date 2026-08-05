package webhook

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/adcp/v3"
	"github.com/adcontextprotocol/adcp-go/adcp/v3/idempotency"
)

func TestMarshalGeneratesKeyWhenEmpty(t *testing.T) {
	p := &adcp.MCPWebhookPayload{
		TaskID:    "task_123",
		TaskType:  "create_media_buy",
		Status:    "completed",
		Timestamp: "2026-04-19T00:00:00Z",
	}
	body, key, err := Marshal(p)
	require.NoError(t, err)
	require.NotEmpty(t, key)

	require.NoError(t, idempotency.Validate(key))
	assert.Equal(t, key, p.IdempotencyKey, "Marshal must populate the struct in place so senders can read it back")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, key, decoded["idempotency_key"])
}

func TestMarshalPreservesCallerKey(t *testing.T) {
	const caller = "whk_01HW9D2T3VXQ5M7K9N1P3R5S7U"
	p := &adcp.CollectionListChangedWebhook{
		IdempotencyKey: caller,
		Event:          "collection_list_changed",
		ListID:         "list_1",
		ResolvedAt:     "2026-04-19T00:00:00Z",
		Signature:      "hmac",
	}
	_, key, err := Marshal(p)
	require.NoError(t, err)
	assert.Equal(t, caller, key)
}

func TestMarshalRejectsMalformedCallerKey(t *testing.T) {
	p := &adcp.PropertyListChangedWebhook{
		IdempotencyKey: "short", // under the 16-char minimum
		Event:          "property_list_changed",
		ListID:         "list_1",
		ResolvedAt:     "2026-04-19T00:00:00Z",
		Signature:      "hmac",
	}
	_, _, err := Marshal(p)
	require.Error(t, err)
	var invalid *idempotency.InvalidKeyError
	assert.ErrorAs(t, err, &invalid)
}

func TestMarshalAllFivePayloadTypes(t *testing.T) {
	// Every type with IdempotencyKey must satisfy Payload — this would fail
	// to compile if any of the five methods in webhook_payloads.go disappeared.
	cases := []struct {
		name string
		p    Payload
	}{
		{"mcp", &adcp.MCPWebhookPayload{TaskID: "t", TaskType: "x", Status: "s", Timestamp: "2026-04-19T00:00:00Z"}},
		{"collection", &adcp.CollectionListChangedWebhook{Event: "collection_list_changed", ListID: "l", ResolvedAt: "2026-04-19T00:00:00Z", Signature: "s"}},
		{"property", &adcp.PropertyListChangedWebhook{Event: "property_list_changed", ListID: "l", ResolvedAt: "2026-04-19T00:00:00Z", Signature: "s"}},
		{"artifact", &adcp.ArtifactWebhookPayload{MediaBuyID: "mb", BatchID: "b", Timestamp: "2026-04-19T00:00:00Z"}},
		{"revocation", &adcp.RevocationNotification{RightsID: "r", BrandID: "b", Reason: "x", EffectiveAt: "2026-04-19T00:00:00Z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, key, err := Marshal(tc.p)
			require.NoError(t, err)
			require.NoError(t, idempotency.Validate(key))
		})
	}
}
