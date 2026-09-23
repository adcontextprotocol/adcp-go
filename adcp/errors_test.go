package adcp

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recoveryEnum is the closed set core/error.json defines for the recovery
// field. A receiver that does not recognise an error code is required to read
// recovery for its retry classification, so a value outside this set leaves
// the caller with no machine-readable signal at all.
var recoveryEnum = map[string]bool{
	"transient":   true,
	"correctable": true,
	"terminal":    true,
}

func TestDefaultRecovery(t *testing.T) {
	// Expected values are the published enumMetadata classifications from the
	// protocol bundle this module pins (adcp/schemas/VERSION). The three codes
	// marked SDK-internal are not published in enums/error-code.json at any 3.x
	// version; their classification is this SDK's own and is asserted here so a
	// future change to it is deliberate.
	tests := []struct {
		code string
		want string
	}{
		{"RATE_LIMITED", "transient"},
		{"SERVICE_UNAVAILABLE", "transient"},
		{"BUDGET_TOO_LOW", "correctable"},
		{"INVALID_REQUEST", "correctable"},
		{"TERMS_REJECTED", "correctable"},
		{"ACCOUNT_NOT_FOUND", "terminal"},
		{"MISSING_FIELD", "correctable"}, // SDK-internal, unpublished
		{"INVALID_FIELD", "correctable"}, // SDK-internal, unpublished
		{"INTERNAL_ERROR", "terminal"},   // SDK-internal, unpublished
		{"SOME_SELLER_SPECIFIC_CODE", "terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := defaultRecovery(tt.code)
			if got != tt.want {
				t.Errorf("defaultRecovery(%q) = %q, want %q", tt.code, got, tt.want)
			}
			if !recoveryEnum[got] {
				t.Errorf("defaultRecovery(%q) = %q, which is not a member of the recovery enum", tt.code, got)
			}
		})
	}
}

// TestErrorRecoveryOnWire pins the value that actually reaches a receiver by
// reading it back out of the marshalled result. defaultRecovery is unexported
// and its return is re-marshalled by Error, so asserting the switch alone would
// not prove the wire contract.
func TestErrorRecoveryOnWire(t *testing.T) {
	cases := []struct {
		code string
		opts ErrorOptions
		want string
	}{
		{code: "RATE_LIMITED", want: "transient"},
		{code: "SERVICE_UNAVAILABLE", want: "transient"},
		{code: "BUDGET_TOO_LOW", want: "correctable"},
		{code: "ACCOUNT_NOT_FOUND", want: "terminal"},
		{code: "UNRECOGNIZED_CODE", want: "terminal"},
		// An explicit Recovery still wins over the default, so a caller
		// following the documented vocabulary must reach the wire intact.
		{code: "TERMS_REJECTED", opts: ErrorOptions{Recovery: "correctable"}, want: "correctable"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			opts := tc.opts
			opts.Message = "test"
			result, _, err := Error[any](tc.code, opts)
			if err != nil {
				t.Fatalf("Error(%q) returned err %v", tc.code, err)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("Error(%q) content[0] is %T, want *mcp.TextContent", tc.code, result.Content[0])
			}
			var wrapper adcpErrorWrapper
			if err := json.Unmarshal([]byte(text.Text), &wrapper); err != nil {
				t.Fatalf("unmarshal %q: %v", text.Text, err)
			}
			got := wrapper.ADCPError.Recovery
			if got != tc.want {
				t.Errorf("wire recovery for %q = %q, want %q", tc.code, got, tc.want)
			}
			if !recoveryEnum[got] {
				t.Errorf("wire recovery for %q = %q, which is not a member of the recovery enum", tc.code, got)
			}
		})
	}
}
