package tmpendpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"base url", "https://identity-agent.example.com", ""},
		{"base url with path prefix", "https://tmp.example.com/v1", ""},
		{"base url with trailing slash", "https://tmp.example.com/v1/", ""},
		{"cleartext for local dev", "http://identity-agent:8080", ""},
		{"identity operation path", "https://identity-agent.example.com/identity", `ending in "/identity"`},
		{"context operation path", "https://context-agent.example.com/context", `ending in "/context"`},
		{"operation path with trailing slash", "https://a.example.com/identity/", `ending in "/identity"`},
		{"operation path with query string", "https://ctx.example.com/context?x=1", `ending in "/context"`},
		{"operation path with fragment", "https://ctx.example.com/context#f", `ending in "/context"`},
		{"empty", "", "must not be empty"},
		{"whitespace only", "   ", "must not be empty"},
		{"no scheme", "identity-agent.example.com", "http or https scheme"},
		{"host only with port", "identity-agent:8080", "http or https scheme"},
		{"scheme without host", "https://", "must be absolute"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.raw)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
