//go:build goexperiment.jsonv2

package adcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encoding/json/v2 is on by default from Go 1.27 and behind GOEXPERIMENT=jsonv2
// before. These tests run it natively, not through the v1 API. The import sits
// in two small files because Go 1.27 refuses the package in a file whose Go
// version is older.

func TestOptionalWithNativeJSONv2KeepsThreeStates(t *testing.T) {
	for _, tc := range []struct {
		wire  string
		state string
	}{
		{`{}`, "omitted"},
		{`{"cap":null}`, "null"},
		{`{"cap":5}`, "value:5"},
		{`{"cap":0}`, "value:0"},
	} {
		t.Run(tc.wire, func(t *testing.T) {
			var f optionalFixture
			require.NoError(t, jsonv2Unmarshal([]byte(tc.wire), &f))
			assert.Equal(t, tc.state, optionalStateOf(f.Cap))

			out, err := jsonv2Marshal(f)
			require.NoError(t, err)
			assert.Equal(t, tc.wire, string(out))
		})
	}
}

func TestOptionalWithNativeJSONv2OmitemptyDropsNull(t *testing.T) {
	// Native v2 redefines omitempty to drop a value that encodes as null, so
	// a field tagged omitempty,omitzero loses the null state. The tag must be
	// omitzero only.
	out, err := jsonv2Marshal(struct {
		Cap Optional[float64] `json:"cap,omitempty,omitzero"`
	}{Null[float64]()})
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(out))

	out, err = jsonv2Marshal(struct {
		Cap Optional[float64] `json:"cap,omitzero"`
	}{Null[float64]()})
	require.NoError(t, err)
	assert.Equal(t, `{"cap":null}`, string(out))
}

func TestOptionalWithNativeJSONv2RefusesUnsetAndNil(t *testing.T) {
	_, err := jsonv2Marshal(struct {
		Cap Optional[float64] `json:"cap"`
	}{})
	assert.ErrorIs(t, err, ErrOptionalUnset)

	_, err = jsonv2Marshal(optionalFixture{Tags: Some[[]string](nil)})
	assert.ErrorIs(t, err, ErrOptionalNilValue)
}
