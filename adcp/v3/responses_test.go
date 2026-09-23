package adcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaBuyResponseUsesCreateSuccessFields(t *testing.T) {
	result, out, err := MediaBuyResponse(&CreateMediaBuySuccess{
		MediaBuyID:      "mb-1",
		MediaBuyStatus:  "active",
		Packages:        []Package{{PackageID: "pkg-1"}},
		ValidActions:    []string{"pause", "cancel"},
		PlannedDelivery: &PlannedDelivery{TotalBudget: 1000},
		Ext:             map[string]any{"status": "wrong"},
	})
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "active", wire["media_buy_status"])
	assert.Equal(t, []any{"pause", "cancel"}, wire["valid_actions"])
	assert.Equal(t, map[string]any{"total_budget": float64(1000)}, wire["planned_delivery"])
	assert.Equal(t, map[string]any{"status": "wrong"}, wire["ext"])
	assert.IsType(t, &CreateMediaBuySuccess{}, out)
}

func TestMediaBuyResponseSubmittedUsesTypedTaskFields(t *testing.T) {
	result, out, err := MediaBuyResponse(&CreateMediaBuySubmitted{
		Status:  "submitted",
		TaskID:  "task-1",
		Message: "queued",
	})
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "submitted", wire["status"])
	assert.Equal(t, "task-1", wire["task_id"])
	assert.Equal(t, "queued", wire["message"])
	assert.NotContains(t, wire, "media_buy_id")
	assert.IsType(t, &CreateMediaBuySubmitted{}, out)
}

func TestMediaBuyResponseAcceptsValueVariants(t *testing.T) {
	var alias CreateMediaBuyResult = CreateMediaBuySuccess{
		MediaBuyID: "mb-1",
		Packages:   []Package{{PackageID: "pkg-1"}},
	}
	result, out, err := MediaBuyResponse(alias)
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "mb-1", wire["media_buy_id"])
	assert.IsType(t, &CreateMediaBuySuccess{}, out)

	result, out, err = MediaBuyResponse(CreateMediaBuySubmitted{TaskID: "task-1"})
	require.NoError(t, err)
	wire, ok = result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "submitted", wire["status"])
	assert.IsType(t, &CreateMediaBuySubmitted{}, out)

	result, out, err = MediaBuyResponse(CreateMediaBuyError{
		Errors: []AdcpError{{"code": "INVALID_REQUEST", "message": "bad request"}},
	})
	require.NoError(t, err)
	wire, ok = result.StructuredContent.(map[string]any)
	require.True(t, ok)
	assert.True(t, result.IsError)
	assert.Contains(t, wire, "errors")
	assert.IsType(t, &CreateMediaBuyError{}, out)
}

func TestMediaBuyResponseSubmittedRequiresTaskID(t *testing.T) {
	result, out, err := MediaBuyResponse(&CreateMediaBuySubmitted{Status: "submitted"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Nil(t, out)
	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	errObj, ok := wire["adcp_error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "MISSING_FIELD", errObj["code"])
	assert.Equal(t, "task_id", errObj["field"])
}

func TestMediaBuyResponsePreservesEmptyValidActions(t *testing.T) {
	result, _, err := MediaBuysDataResponse(&GetMediaBuysResponse{
		MediaBuys: []MediaBuyData{{
			MediaBuyID:   "mb-1",
			Status:       "canceled",
			Currency:     "USD",
			TotalBudget:  100,
			Packages:     []PackageStatus{{Package: Package{PackageID: "pkg-1"}}},
			ValidActions: []string{},
		}},
	})
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	buys, ok := wire["media_buys"].([]any)
	require.True(t, ok)
	require.Len(t, buys, 1)
	buy, ok := buys[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{}, buy["valid_actions"])
}

// TestMediaBuysDataResponseExtDoesNotOverwriteTypedFields proves that an Ext
// key colliding with a canonical MediaBuyData field name (e.g. "currency",
// "valid_actions") cannot clobber the typed field on the wire: Ext always
// marshals as its own nested "ext" object rather than being merged into the
// parent object, the same guarantee TestMediaBuyResponseUsesCreateSuccessFields
// above already proves for CreateMediaBuySuccess. See adcp-go#162.
func TestMediaBuysDataResponseExtDoesNotOverwriteTypedFields(t *testing.T) {
	result, _, err := MediaBuysDataResponse(&GetMediaBuysResponse{
		MediaBuys: []MediaBuyData{{
			MediaBuyID:   "mb-1",
			Status:       "active",
			Currency:     "USD",
			TotalBudget:  100,
			Packages:     []PackageStatus{{Package: Package{PackageID: "pkg-1"}}},
			ValidActions: []string{"cancel"},
			Ext: map[string]any{
				"currency":      "EUR",
				"valid_actions": []string{"should-not-win"},
			},
		}},
	})
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	buys, ok := wire["media_buys"].([]any)
	require.True(t, ok)
	require.Len(t, buys, 1)
	buy, ok := buys[0].(map[string]any)
	require.True(t, ok)

	assert.Equal(t, "USD", buy["currency"], "typed Currency must win over a colliding Ext key")
	assert.Equal(t, []any{"cancel"}, buy["valid_actions"], "typed ValidActions must win over a colliding Ext key")
	assert.Equal(t, map[string]any{
		"currency":      "EUR",
		"valid_actions": []any{"should-not-win"},
	}, buy["ext"], "Ext is preserved verbatim in its own nested object, not merged into the parent")
}

func TestMediaBuysResponseNilListEmitsEmptyArray(t *testing.T) {
	result, _, err := MediaBuysResponse(nil, true)
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	buys, ok := wire["media_buys"].([]any)
	require.True(t, ok)
	assert.Empty(t, buys)
	assert.Equal(t, true, wire["sandbox"])
}

func TestMediaBuysDataResponseIncludesPackageStatusFields(t *testing.T) {
	result, _, err := MediaBuysDataResponse(&GetMediaBuysResponse{
		MediaBuys: []MediaBuyData{{
			MediaBuyID:  "mb-1",
			Status:      "active",
			Currency:    "USD",
			TotalBudget: 100,
			InvoiceRecipient: &BusinessEntity{
				LegalName: "Acme Corporation",
			},
			Packages: []PackageStatus{{
				Package: Package{
					PackageID:           "pkg-1",
					PricingOptionID:     "po-1",
					CreativeAssignments: []CreativeAssignment{{CreativeID: "cr-hidden"}},
				},
				CreativeApprovals: []PackageCreativeApproval{{
					CreativeID:     "cr-1",
					ApprovalStatus: "approved",
				}},
				FormatIDsPending: []FormatRef{{AgentURL: "https://seller.example", ID: "display"}},
				Snapshot: &PackageSnapshot{
					AsOf:             "2026-05-25T12:00:00Z",
					StalenessSeconds: 30,
					Impressions:      1000,
					Spend:            12.5,
					Currency:         "USD",
				},
			}},
		}},
	})
	require.NoError(t, err)

	wire, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok)
	buys, ok := wire["media_buys"].([]any)
	require.True(t, ok)
	require.Len(t, buys, 1)
	buy, ok := buys[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"legal_name": "Acme Corporation"}, buy["invoice_recipient"])
	packages, ok := buy["packages"].([]any)
	require.True(t, ok)
	require.Len(t, packages, 1)
	pkg, ok := packages[0].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, pkg, "creative_approvals")
	assert.Contains(t, pkg, "format_ids_pending")
	assert.Contains(t, pkg, "snapshot")
	assert.NotContains(t, pkg, "pricing_option_id")
	assert.NotContains(t, pkg, "creative_assignments")
}
