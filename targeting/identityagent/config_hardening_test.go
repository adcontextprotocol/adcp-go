package identityagent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTmpxSlotIDs_ValidatesCharsetLenUniq pins the schema-derived
// startup validation for TMPX_SLOT_IDS: entries MUST match
// tmpx-chunk.json §slot_id (^[a-zA-Z][a-zA-Z0-9_]*$), stay within its
// maxLength: 64, and be unique. A misconfig here previously passed the
// agent's split/trim/cap check and was only caught at serve time by the
// router's slot-contract validator, which drops the provider's chunks
// atomically — silently zeroing TMPX in production.
func TestParseTmpxSlotIDs_ValidatesCharsetLenUniq(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"empty is legal", "", ""},
		{"single_ok", "primary", ""},
		{"two_ok", "primary,secondary", ""},
		{"leading_digit_bad", "1st", "must match"},
		{"hyphen_bad", "first-slot", "must match"},
		{"dot_bad", "first.slot", "must match"},
		{"duplicate_bad", "same,same", "duplicated"},
		{"oversize_bad", strings.Repeat("a", 65), "exceeds the schema's 64-char"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTmpxSlotIDs(tc.raw)
			if tc.wantErr == "" {
				require.NoError(t, err)
				if tc.raw != "" {
					assert.NotEmpty(t, got)
				}
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
