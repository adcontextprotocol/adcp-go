package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResponseBuildersEmitEnvelopeStatus covers #612: every response
// builder in responses.go must emit the protocol-envelope status the
// response schemas require. Builders that already default it
// (CapabilitiesResponse, ProductsResponse, the reporting builders) are
// included so the whole surface stays pinned.
func TestResponseBuildersEmitEnvelopeStatus(t *testing.T) {
	statusOf := func(t *testing.T, out any) string {
		t.Helper()
		b, err := json.Marshal(out)
		require.NoError(t, err)
		var wire map[string]any
		require.NoError(t, json.Unmarshal(b, &wire))
		status, _ := wire["status"].(string)
		return status
	}

	cases := []struct {
		name  string
		build func() (any, error)
	}{
		{"CapabilitiesResponse", func() (any, error) { _, out, err := CapabilitiesResponse(&CapabilitiesData{}); return out, err }},
		{"ProductsResponse", func() (any, error) { _, out, err := ProductsResponse(&ProductsData{}); return out, err }},
		{"MediaBuysResponse", func() (any, error) { _, out, err := MediaBuysResponse(nil, false); return out, err }},
		{"DeliveryResponse", func() (any, error) {
			_, out, err := DeliveryResponse(&DeliveryData{Currency: "USD", MediaBuyDeliveries: []MediaBuyDelivery{}})
			return out, err
		}},
		{"SyncAccountsResponse", func() (any, error) { _, out, err := SyncAccountsResponse(nil, false); return out, err }},
		{"GovernanceResponse", func() (any, error) { _, out, err := GovernanceResponse(nil); return out, err }},
		{"SyncCreativesResponse", func() (any, error) { _, out, err := SyncCreativesResponse(nil, false); return out, err }},
		{"ListCreativesResponse", func() (any, error) { _, out, err := ListCreativesResponse(nil); return out, err }},
		{"PreviewCreativeResponse", func() (any, error) {
			_, out, err := PreviewCreativeResponse("c1", "n", "https://example.com/p", 300, 250)
			return out, err
		}},
		{"BuildCreativeResponse", func() (any, error) { _, out, err := BuildCreativeResponse(map[string]any{}, false); return out, err }},
		{"CreativeFormatsResponse", func() (any, error) { _, out, err := CreativeFormatsResponse(nil, false); return out, err }},
		{"SignalsResponse", func() (any, error) { _, out, err := SignalsResponse(nil, false); return out, err }},
		{"ActivateSignalResponse", func() (any, error) { _, out, err := ActivateSignalResponse(nil, false); return out, err }},
		{"SyncCatalogsResponse", func() (any, error) { _, out, err := SyncCatalogsResponse(nil, false); return out, err }},
		{"SyncEventSourcesResponse", func() (any, error) { _, out, err := SyncEventSourcesResponse(nil, false); return out, err }},
		{"LogEventResponse", func() (any, error) { _, out, err := LogEventResponse(1, 1, 0, false); return out, err }},
		{"PerformanceFeedbackResponse", func() (any, error) { _, out, err := PerformanceFeedbackResponse(false); return out, err }},
		{"CreateCollectionListResponse", func() (any, error) {
			_, out, err := CreateCollectionListResponse(&CollectionList{Name: "l"}, "tok")
			return out, err
		}},
		{"GetCollectionListResponse", func() (any, error) {
			_, out, err := GetCollectionListResponse(&CollectionList{Name: "l"}, nil, nil)
			return out, err
		}},
		{"UpdateCollectionListResponse", func() (any, error) {
			_, out, err := UpdateCollectionListResponse(&CollectionList{Name: "l"})
			return out, err
		}},
		{"DeleteCollectionListResponse", func() (any, error) { _, out, err := DeleteCollectionListResponse("l1"); return out, err }},
		{"ListCollectionListsResponse", func() (any, error) { _, out, err := ListCollectionListsResponse(nil, nil); return out, err }},
		{"SyncReportingStatusResponseData", func() (any, error) { _, out, err := SyncReportingStatusResponseData(nil); return out, err }},
		{"SyncReportingReceiptsResponseData", func() (any, error) { _, out, err := SyncReportingReceiptsResponseData(nil); return out, err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.build()
			require.NoError(t, err)
			assert.Equal(t, "completed", statusOf(t, out))
		})
	}

	// The create_media_buy success branch is a generated type with no
	// Status field; its builder adds the envelope status on the wire
	// while still returning the typed value to Go callers.
	result, out, err := MediaBuyResponse(&CreateMediaBuySuccess{MediaBuyID: "mb-1", MediaBuyStatus: "active", ConfirmedAt: Ptr("2026-06-01T00:00:00Z"), Revision: 1})
	require.NoError(t, err)
	assert.IsType(t, &CreateMediaBuySuccess{}, out)
	assert.Equal(t, "completed", statusOf(t, result.StructuredContent))
}

// TestDeliveryResponseStatus validates a delivery response against the
// bundled schema: DeliveryData now carries the envelope status and the
// builder defaults it to completed without mutating the caller's value.
func TestDeliveryResponseStatus(t *testing.T) {
	data := &DeliveryData{
		ReportingPeriod:    ReportingPeriod{Start: "2026-06-01", End: "2026-06-30"},
		Currency:           "USD",
		MediaBuyDeliveries: []MediaBuyDelivery{},
	}
	result, out, err := DeliveryResponse(data)
	require.NoError(t, err)
	assert.Equal(t, TaskStatus(""), data.Status, "builder must not mutate the caller's DeliveryData")
	typed, ok := out.(*DeliveryData)
	require.True(t, ok)
	assert.Equal(t, TaskStatusCompleted, typed.Status)
	assertEncodingSchemaValid(t, "media-buy/get-media-buy-delivery-response.json", result.StructuredContent)

	// An explicit status is preserved.
	_, out, err = DeliveryResponse(&DeliveryData{Status: TaskStatusWorking, Currency: "USD", MediaBuyDeliveries: []MediaBuyDelivery{}})
	require.NoError(t, err)
	assert.Equal(t, TaskStatusWorking, out.(*DeliveryData).Status)
}
