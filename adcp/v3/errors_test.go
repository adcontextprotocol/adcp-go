package adcp

import "testing"

func TestDefaultRecoveryIsInEnum(t *testing.T) {
	tests := map[string]string{
		"RATE_LIMITED":           "transient",
		"SERVICE_UNAVAILABLE":    "transient",
		"BUDGET_TOO_LOW":         "correctable",
		"INVALID_REQUEST":        "correctable",
		"TERMS_REJECTED":         "correctable",
		"ACCOUNT_SETUP_REQUIRED": "correctable",
		"ACCOUNT_NOT_FOUND":      "terminal",
		"INTERNAL_ERROR":         "terminal",
		"SELLER_SPECIFIC":        "terminal",
	}
	for code, want := range tests {
		if got := defaultRecovery(code); got != want {
			t.Errorf("defaultRecovery(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestNormalizeRecovery(t *testing.T) {
	tests := map[string]string{
		"retry": "transient", "revise": "correctable", "contact_support": "terminal",
		"transient": "transient", "correctable": "correctable", "terminal": "terminal",
		"bogus": "bogus",
	}
	for in, want := range tests {
		if got := normalizeRecovery(in); got != want {
			t.Errorf("normalizeRecovery(%q) = %q, want %q", in, got, want)
		}
	}
}
