package router

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoFollowRedirect_ReturnsErrUseLastResponse pins the helper
// contract: TMP forbids redirects on provider endpoints, so every
// router-side HTTP client returns http.ErrUseLastResponse from its
// CheckRedirect hook. That sentinel makes net/http surface the 3xx
// response to the caller instead of transparently following it.
func TestNoFollowRedirect_ReturnsErrUseLastResponse(t *testing.T) {
	assert.ErrorIs(t, noFollowRedirect(nil, nil), http.ErrUseLastResponse)
}

// TestRouter_DefaultClient_DoesNotFollowRedirect drives the router's
// default fan-out client at a redirect-emitting httptest server and
// asserts the router observes the 3xx directly and the canary target
// is never contacted. If CheckRedirect were left unset, net/http would
// follow the 3xx and replay the signed request body — identity tokens,
// sealed credentials, artifact bytes — at the canary. safeDialContext
// blocks only private destinations; a public attacker-controlled host
// would still resolve, so the redirect refusal is the load-bearing
// guarantee.
func TestRouter_DefaultClient_DoesNotFollowRedirect(t *testing.T) {
	var canaryHits atomic.Int32
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		canaryHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer canary.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canary.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	router, err := NewRouter(nil, nil, nil, WithoutEndpointValidation())
	require.NoError(t, err)
	require.NotNil(t, router.client.CheckRedirect, "router default client must set CheckRedirect")

	resp, err := router.client.Get(redirector.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, "router client MUST surface the 3xx, not follow it")
	assert.Zero(t, canaryHits.Load(), "redirect target MUST NOT receive the replayed request")
}

// TestHealthChecker_DefaultClient_HasNoFollowRedirect pins the same
// guarantee for provider /health probes. A provider that answers 3xx
// to /health with a location outside its own registered endpoint could
// otherwise trick the router into probing an attacker-controlled host.
func TestHealthChecker_DefaultClient_HasNoFollowRedirect(t *testing.T) {
	hc := NewHealthChecker(NewProviderSet(nil), NewProviderHealth(3, 30*time.Second), HealthCheckConfig{})
	require.NotNil(t, hc.client.CheckRedirect, "health-check default client must set CheckRedirect")
	assert.ErrorIs(t, hc.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}

// TestDiscovery_DefaultClient_HasNoFollowRedirect pins the same
// guarantee for the discovery poll. The discovery response can carry
// arbitrary provider endpoints, so a 3xx on the poll itself would let
// a malicious registry re-target the poll to a host that then feeds
// the router endpoints of its choosing.
func TestDiscovery_DefaultClient_HasNoFollowRedirect(t *testing.T) {
	d := NewDiscovery(NewProviderSet(nil), NewProviderHealth(3, 30*time.Second), DiscoveryConfig{}, 0)
	require.NotNil(t, d.client.CheckRedirect, "discovery default client must set CheckRedirect")
	assert.ErrorIs(t, d.client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}
