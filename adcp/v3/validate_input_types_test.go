package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneratedValidateInputTypesRoundTrip(t *testing.T) {
	req := ValidateInputRequest{
		Manifest: CreativeManifest{},
		Targets: []ValidateInputTarget{
			{Kind: "canonical", ID: "image"},
			{Kind: "product", ID: "prod_123"},
		},
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"targets":[{"kind":"canonical","id":"image"},{"kind":"product","id":"prod_123"}]`)

	wire := []byte(`{
		"status": "completed",
		"results": [{
			"target": {"kind": "canonical", "id": "image"},
			"result_kind": "validated_fail",
			"violations": [{"rule": "aspect_ratio", "field": "assets.hero", "expected": "9:16", "predicted": 1.78}],
			"warnings": [{"rule": "lean_hover_expansion", "field": "assets.hero"}]
		}]
	}`)
	var resp ValidateInputResponse
	require.NoError(t, json.Unmarshal(wire, &resp))
	require.Len(t, resp.Results, 1)

	result := resp.Results[0]
	assert.Equal(t, ValidateInputResultTarget{Kind: "canonical", ID: "image"}, result.Target)
	assert.Equal(t, "validated_fail", result.ResultKind)
	require.Len(t, result.Violations, 1)
	assert.Equal(t, "aspect_ratio", result.Violations[0].Rule)
	assert.Equal(t, "9:16", result.Violations[0].Expected)
	assert.Equal(t, 1.78, result.Violations[0].Predicted)
	require.Len(t, result.Warnings, 1)
	assert.Equal(t, "lean_hover_expansion", result.Warnings[0].Rule)
}
