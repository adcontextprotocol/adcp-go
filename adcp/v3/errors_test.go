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
	// protocol bundle this module pins (adcp/v3/schemas/VERSION). The three codes
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

// TestNormalizeRecovery pins the legacy -> enum mapping for explicit
// Recovery values. "retry"/"revise"/"contact_support" were the SDK's old
// documented vocabulary (issue #530); existing integrations may still send
// them, and they must not reach the wire verbatim.
func TestNormalizeRecovery(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"retry", "transient"},
		{"revise", "correctable"},
		{"contact_support", "terminal"},
		// Current enum members pass through unchanged.
		{"transient", "transient"},
		{"correctable", "correctable"},
		{"terminal", "terminal"},
		// Unknown values are not silently reclassified.
		{"retry_later", "retry_later"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeRecovery(tt.in); got != tt.want {
				t.Errorf("normalizeRecovery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestErrorLegacyRecoveryOnWire proves an explicitly-supplied legacy value
// is rewritten before marshalling: the wire value must be an enum member.
func TestErrorLegacyRecoveryOnWire(t *testing.T) {
	cases := []struct {
		recovery string
		want     string
	}{
		{"retry", "transient"},
		{"revise", "correctable"},
		{"contact_support", "terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.recovery, func(t *testing.T) {
			result, _, err := Error[any]("INVALID_REQUEST",
				ErrorOptions{Message: "test", Recovery: tc.recovery})
			if err != nil {
				t.Fatalf("Error returned err %v", err)
			}
			text, ok := result.Content[0].(*mcp.TextContent)
			if !ok {
				t.Fatalf("content[0] is %T, want *mcp.TextContent", result.Content[0])
			}
			var wrapper adcpErrorWrapper
			if err := json.Unmarshal([]byte(text.Text), &wrapper); err != nil {
				t.Fatalf("unmarshal %q: %v", text.Text, err)
			}
			got := wrapper.ADCPError.Recovery
			if got != tc.want {
				t.Errorf("wire recovery = %q, want %q", got, tc.want)
			}
			if !recoveryEnum[got] {
				t.Errorf("wire recovery = %q, which is not a member of the recovery enum", got)
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
