package adcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeADCPVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "release precision", in: "3.1", want: "3.1", ok: true},
		{name: "full semver", in: "3.1.0", want: "3.1", ok: true},
		{name: "pre release", in: "3.1.0-rc.3", want: "3.1-rc.3", ok: true},
		{name: "build metadata", in: "3.1.2+scope.deploy.4821", want: "3.1", ok: true},
		{name: "invalid", in: "3", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeADCPVersion(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVersionEnvelopeFor(t *testing.T) {
	env, ok := VersionEnvelopeFor("3.1.0-rc.3")

	require.True(t, ok)
	assert.Equal(t, "3.1-rc.3", env.AdcpVersion)
	assert.Equal(t, 3, env.AdcpMajorVersion)
}

func TestNegotiateADCPVersion(t *testing.T) {
	tests := []struct {
		name           string
		requestVersion string
		requestMajor   int
		supported      []string
		want           string
		ok             bool
	}{
		{name: "explicit 3.0", requestVersion: "3.0", want: "3.0", ok: true},
		{name: "explicit 3.1", requestVersion: "3.1", want: "3.1", ok: true},
		{name: "explicit 3.2", requestVersion: "3.2", want: "3.2", ok: true},
		{name: "legacy major", requestMajor: 3, want: "3.2", ok: true},
		{name: "default highest", want: "3.2", ok: true},
		{name: "downshift", requestVersion: "3.1", supported: []string{"3.0"}, want: "3.0", ok: true},
		{name: "pre release uses matching stable", requestVersion: "3.1-rc.3", supported: []string{"3.0", "3.1"}, want: "3.1", ok: true},
		{name: "ga buyer can use only matching pre release seller", requestVersion: "3.1.0", supported: []string{"3.1-rc.3"}, want: "3.1-rc.3", ok: true},
		{name: "cross major unsupported", requestVersion: "4.0", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NegotiateADCPVersion(tt.requestVersion, tt.requestMajor, tt.supported)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNegotiateADCPVersionMajorPresence(t *testing.T) {
	tests := []struct {
		name string
		req  adcpVersionRequest
		want string
		ok   bool
	}{
		{name: "omitted major defaults highest", req: adcpVersionRequest{}, want: "3.2", ok: true},
		{name: "explicit zero major is invalid", req: adcpVersionRequest{major: 0, majorProvided: true}, ok: false},
		{name: "negative major is invalid", req: adcpVersionRequest{major: -1, majorProvided: true}, ok: false},
		{name: "unsupported positive major is invalid", req: adcpVersionRequest{major: 4, majorProvided: true}, ok: false},
		{name: "supported major selects highest matching release", req: adcpVersionRequest{major: 3, majorProvided: true}, want: "3.2", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := negotiateADCPVersion(tt.req, nil)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGeneratedRequestsDecode30And31VersionEnvelopes(t *testing.T) {
	var products GetProductsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"adcp_version": "3.0",
		"adcp_major_version": 3,
		"buying_mode": "brief",
		"brief": "Launch a short test campaign."
	}`), &products))
	assert.Equal(t, "3.0", products.AdcpVersion)
	assert.Equal(t, 3, products.AdcpMajorVersion)
	assert.Equal(t, "brief", products.BuyingMode)

	var create CreateMediaBuyRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"adcp_version": "3.1",
		"adcp_major_version": 3,
		"idempotency_key": "idem-1",
		"account": {"brand": {"domain": "example.com"}, "operator": "buyer"},
		"brand": {"domain": "example.com"},
		"start_time": "2026-06-01T00:00:00Z",
		"end_time": "2026-06-30T00:00:00Z",
		"packages": [{
			"product_id": "prod-1",
			"budget": 1000,
			"pricing_option_id": "cpm-1",
			"format_option_refs": [{"scope": "product", "format_option_id": "display.standard"}]
		}]
	}`), &create))
	assert.Equal(t, "3.1", create.AdcpVersion)
	assert.Equal(t, 3, create.AdcpMajorVersion)
	require.Len(t, create.Packages, 1)
	require.Len(t, create.Packages[0].FormatOptionRefs, 1)
	assert.Equal(t, "display.standard", create.Packages[0].FormatOptionRefs[0].FormatOptionID)
}
