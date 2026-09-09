package router

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestMatchesIdentityProvider_EmptyCountry_FailsClosed pins the fix:
// a request with no `country` MUST NOT match a provider that declared
// `countries`. Before the fix, empty country short-circuited the
// residency filter and every declared-countries provider was fanned
// out to — the opposite of the data-residency guarantee the schema's
// §countries surface exists to make.
func TestMatchesIdentityProvider_EmptyCountry_FailsClosed(t *testing.T) {
	provider := &ProviderConfig{
		IdentityMatch: true,
		Countries:     []string{"US", "CA"},
	}
	req := &tmproto.IdentityMatchRequest{
		Country: "", // omitted
	}
	assert.False(t, MatchesIdentityProvider(req, provider),
		"provider that declares countries must not match a request with empty country")
}

// TestMatchesIdentityProvider_MatchingCountry_Passes pins the positive
// path: a request whose country is in the provider's list still
// matches after the R6 fix.
func TestMatchesIdentityProvider_MatchingCountry_Passes(t *testing.T) {
	provider := &ProviderConfig{
		IdentityMatch: true,
		Countries:     []string{"US", "CA"},
		UIDTypes:      []string{"uid2"},
	}
	req := &tmproto.IdentityMatchRequest{
		Country:    "US",
		Identities: []tmproto.IdentityToken{{UIDType: tmproto.UIDTypeUID2, UserToken: "tok"}},
	}
	assert.True(t, MatchesIdentityProvider(req, provider))
}

// TestMatchesIdentityProvider_ProviderWithoutCountries_MatchesAnything
// pins that a provider that omits `countries` opts out of residency
// filtering and matches every request (including empty country) — the
// legitimate "country-agnostic provider" case.
func TestMatchesIdentityProvider_ProviderWithoutCountries_MatchesAnything(t *testing.T) {
	provider := &ProviderConfig{
		IdentityMatch: true,
		Countries:     nil,
		UIDTypes:      []string{"uid2"},
	}
	req := &tmproto.IdentityMatchRequest{
		Country:    "",
		Identities: []tmproto.IdentityToken{{UIDType: tmproto.UIDTypeUID2, UserToken: "tok"}},
	}
	assert.True(t, MatchesIdentityProvider(req, provider),
		"provider without countries must match a country-less request")
}

// TestValidateProviderConfig_IDCharset pins the charset from
// provider-registration.json §provider_id (^[A-Za-z0-9_]+$). Before
// this check, an out-of-charset ID would land as a signals_by_provider
// / tmpx_providers map key and the router→publisher response schema
// would reject it downstream, but the router would have already
// signed and fanned out the request. Fail at startup instead.
func TestValidateProviderConfig_IDCharset(t *testing.T) {
	cases := []struct {
		name string
		id   string
		ok   bool
	}{
		{"alphanumeric_underscore", "acme_v2", true},
		{"digits_only", "42", true},
		{"hyphen_rejected", "acme-v2", false},
		{"dot_rejected", "acme.v2", false},
		{"space_rejected", "acme v2", false},
		{"colon_rejected", "acme:v2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProviderConfig(&ProviderConfig{ID: tc.id, ContextMatch: true}, 0)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "provider-registration.json §provider_id")
			}
		})
	}
}

// TestValidateProviderConfig_IDMaxLen pins provider-registration.json's
// maxLength: 64 on §provider_id.
func TestValidateProviderConfig_IDMaxLen(t *testing.T) {
	// Underscore-only so the charset check passes and the length
	// check is the one that fires.
	long := ""
	for range 65 {
		long += "a"
	}
	err := ValidateProviderConfig(&ProviderConfig{ID: long, ContextMatch: true}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum length of 64")
}

// TestValidateProviderConfig_TimeoutMsRange pins the schema's
// timeout_ms range [5, 5000].
func TestValidateProviderConfig_TimeoutMsRange(t *testing.T) {
	base := ProviderConfig{ID: "acme", ContextMatch: true}
	cases := []struct {
		name    string
		timeout time.Duration
		ok      bool
	}{
		{"zero_disables_check", 0, true},
		{"below_min", 3 * time.Millisecond, false},
		{"at_min", 5 * time.Millisecond, true},
		{"middle", 100 * time.Millisecond, true},
		{"at_max", 5000 * time.Millisecond, true},
		{"above_max", 6 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Timeout = tc.timeout
			err := ValidateProviderConfig(&p, 0)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "outside schema range")
			}
		})
	}
}

// TestValidateProviderConfig_PriorityNonNegative pins §priority min: 0.
func TestValidateProviderConfig_PriorityNonNegative(t *testing.T) {
	err := ValidateProviderConfig(&ProviderConfig{ID: "acme", ContextMatch: true, Priority: -1}, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "priority must be >= 0")

	err = ValidateProviderConfig(&ProviderConfig{ID: "acme", ContextMatch: true, Priority: 0}, 0)
	assert.NoError(t, err)
	err = ValidateProviderConfig(&ProviderConfig{ID: "acme", ContextMatch: true, Priority: 100}, 0)
	assert.NoError(t, err)
}

// TestValidateProviderConfig_TmpxSlots pins the tmpx-chunk.json §slot_id
// charset and provider-registration.json's cap/uniqueness constraints on
// §tmpx_slots. A misconfigured slot list would silently fail at serve
// time when the router drops the provider's chunks after slot-contract
// validation — this catches it at startup.
func TestValidateProviderConfig_TmpxSlots(t *testing.T) {
	cases := []struct {
		name  string
		slots []string
		ok    bool
	}{
		{"empty_ok", nil, true},
		{"one_slot", []string{"primary"}, true},
		{"two_slots", []string{"primary", "secondary"}, true},
		{"three_over_cap", []string{"a", "b", "c"}, false},
		{"leading_digit_bad", []string{"1st"}, false},
		{"hyphen_bad", []string{"first-slot"}, false},
		{"duplicate", []string{"same", "same"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ProviderConfig{
				ID:            "acme",
				IdentityMatch: true,
				Countries:     []string{"US"},
				UIDTypes:      []string{"uid2"},
				TmpxSlots:     tc.slots,
			}
			err := ValidateProviderConfig(&p, 0)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
