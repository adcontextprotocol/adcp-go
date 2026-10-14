package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Each fixture validates against its AdCP 3.2.2 schema: the schemas' own
// examples (one result per result_kind and the request), a warning carrying
// the required field, a macro resolution result, and a warning on a pass whose
// field, expected, predicted and retry_with hold zero, false and object values.
// Decoding into the type and encoding back must reproduce each exactly, so a
// dropped field or an omitted zero value fails the test.
func TestGeneratedValidateInputTypesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
		target  any
	}{
		{"validate input result example 0", `{"target":{"kind":"product","id":"streamhaus_vertical_video_us"},"result_kind":"validated_fail","violations":[{"rule":"duration_ms_range","expected":"3000-90000","predicted":95000,"field":"assets.video_main.duration_ms","retry_with":{"trim_ms_from_end":5000}}]}`, &ValidateInputResult{}},
		{"validate input result example 1", `{"target":{"kind":"canonical","id":"video_hosted"},"result_kind":"validated_pass"}`, &ValidateInputResult{}},
		{"validate input result example 2", `{"target":{"kind":"product","id":"nondeterministic_video_vertical_15s"},"result_kind":"unvalidatable_nondeterministic"}`, &ValidateInputResult{}},
		{"validate input request example 0", `{"manifest":{"format_kind":"video_hosted","assets":{"video_main":{"asset_type":"video","url":"https://cdn.acme.example/spring-30s.mp4","duration_ms":30000,"width":1080,"height":1920}},"brand":{"domain":"acme.example"}},"targets":[{"kind":"canonical","id":"video_hosted"},{"kind":"product","id":"streamhaus_vertical_video_us"}]}`, &ValidateInputRequest{}},
		{"validate input response warning with field", `{"status":"completed","results":[{"target":{"kind":"canonical","id":"image"},"result_kind":"validated_fail","violations":[{"rule":"aspect_ratio","field":"assets.hero","expected":"9:16","predicted":1.78}],"warnings":[{"rule":"lean_hover_expansion","field":"assets.hero"}]}]}`, &ValidateInputResponse{}},
		{"validate input response macro resolution result", `{"status":"completed","results":[{"target":{"kind":"capability","id":"img_1"},"result_kind":"validated_pass","macro_resolution_results":[{"declaration_id":"d1","asset_path":"/assets/click","token":"%%X%%","dialect":"unknown","dialect_semantic":"x","mapping_status":"unresolved","operation":"preserve","requested_encoding":{"kind":"none","depth":0},"required":false,"unavailable_behavior":"preserve","status":"preserved_for_downstream","reason":"preserved_unknown"}]}]}`, &ValidateInputResponse{}},
		{"validate input response warning zero values on pass", `{"status":"completed","results":[{"target":{"kind":"canonical","id":"image"},"result_kind":"validated_pass","warnings":[{"rule":"lean_hover_expansion","field":"","expected":0,"predicted":false,"retry_with":{"expansion_trigger":"click"}}]}]}`, &ValidateInputResponse{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, json.Unmarshal([]byte(tc.fixture), tc.target))
			out, err := json.Marshal(tc.target)
			require.NoError(t, err)
			assert.JSONEq(t, tc.fixture, string(out))
		})
	}
}

func TestValidateInputWarningKeepsField(t *testing.T) {
	out, err := json.Marshal(ValidateInputWarning{Rule: "lean_hover_expansion", Field: "assets.hero"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"rule":"lean_hover_expansion","field":"assets.hero"}`, string(out))
}
