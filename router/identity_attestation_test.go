package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// identityCapture records what a fake provider received on its /identity call.
type identityCapture struct {
	body []byte
	sig  string
	kid  string
}

func mkIdentityProvider(slot *atomic.Value) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		slot.Store(identityCapture{
			body: data,
			sig:  r.Header.Get(tmproto.HeaderTMPSignature),
			kid:  r.Header.Get(tmproto.HeaderTMPKeyID),
		})
		_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
			RequestID:          "id-att",
			EligiblePackageIDs: []string{"pkg"},
			ServeWindowSec:     60,
		})
	}))
}

func loadCapture(t *testing.T, slot *atomic.Value) (identityCapture, bool) {
	t.Helper()
	c, ok := slot.Load().(identityCapture)
	if !ok {
		return identityCapture{}, false
	}
	var parsed tmproto.IdentityMatchRequest
	require.NoError(t, json.Unmarshal(c.body, &parsed))
	return c, true
}

// Each provider receives only the identity tokens whose uid_type it declared,
// and the per-provider signature verifies against that filtered set.
func TestRouter_IdentityMatch_FiltersIdentitiesPerProvider(t *testing.T) {
	var capUID2, capID5 atomic.Value
	provUID2 := mkIdentityProvider(&capUID2)
	defer provUID2.Close()
	provID5 := mkIdentityProvider(&capID5)
	defer provID5.Close()

	router, _, ks := newSignedTestRouter(t, []ProviderConfig{
		{ID: "uid2", Endpoint: provUID2.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
		{ID: "id5", Endpoint: provID5.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"id5"}, Timeout: 5 * time.Second},
	})

	body := `{
		"type":"identity_match_request",
		"request_id":"id-att",
		"seller_agent_url":"https://seller.example.com/agent",
		"identities":[{"user_token":"tok2","uid_type":"uid2"},{"user_token":"tok5","uid_type":"id5"}],
		"country":"US"
	}`
	w := httptest.NewRecorder()
	router.HandleIdentityMatch(w, httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(body)))
	require.Equal(t, 200, w.Code, w.Body.String())

	cu, ok := capUID2.Load().(identityCapture)
	require.True(t, ok, "uid2 provider not called")
	ci, ok := capID5.Load().(identityCapture)
	require.True(t, ok, "id5 provider not called")

	var ru, ri tmproto.IdentityMatchRequest
	require.NoError(t, json.Unmarshal(cu.body, &ru))
	require.NoError(t, json.Unmarshal(ci.body, &ri))

	require.Len(t, ru.Identities, 1, "uid2 provider must receive only the uid2 token")
	assert.Equal(t, tmproto.UIDTypeUID2, ru.Identities[0].UIDType)
	require.Len(t, ri.Identities, 1, "id5 provider must receive only the id5 token")
	assert.Equal(t, tmproto.UIDTypeID5, ri.Identities[0].UIDType)

	// The signature each provider received verifies against its filtered body
	// (proving the router re-signed over the subset it actually forwarded).
	require.NoError(t, tmproto.VerifyIdentityMatch(&ru, provUID2.URL, cu.sig, cu.kid, ks, time.Now()))
	require.NoError(t, tmproto.VerifyIdentityMatch(&ri, provID5.URL, ci.sig, ci.kid, ks, time.Now()))
}

// sealed_credentials are routed to the provider owning the audience_kid, never
// broadcast; entries addressed to an unknown audience are dropped.
func TestRouter_IdentityMatch_RoutesSealedCredentialsByAudience(t *testing.T) {
	var capA, capB atomic.Value
	provA := mkIdentityProvider(&capA)
	defer provA.Close()
	provB := mkIdentityProvider(&capB)
	defer provB.Close()

	router, _, ks := newSignedTestRouter(t, []ProviderConfig{
		{ID: "a", Endpoint: provA.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, AudienceKIDs: []string{"k_a"}, Timeout: 5 * time.Second},
		{ID: "b", Endpoint: provB.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, AudienceKIDs: []string{"k_b"}, Timeout: 5 * time.Second},
	})

	body := `{
		"type":"identity_match_request",
		"request_id":"id-att",
		"seller_agent_url":"https://seller.example.com/agent",
		"identities":[{"user_token":"tok","uid_type":"uid2"}],
		"sealed_credentials":[
			{"audience_kid":"k_a","payload":"k_a.AAAA"},
			{"audience_kid":"k_b","payload":"k_b.BBBB"},
			{"audience_kid":"k_orphan","payload":"k_orphan.CCCC"}
		],
		"country":"US"
	}`
	w := httptest.NewRecorder()
	router.HandleIdentityMatch(w, httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(body)))
	require.Equal(t, 200, w.Code, w.Body.String())

	ca, _ := loadCapture(t, &capA)
	cb, _ := loadCapture(t, &capB)
	var ra, rb tmproto.IdentityMatchRequest
	require.NoError(t, json.Unmarshal(ca.body, &ra))
	require.NoError(t, json.Unmarshal(cb.body, &rb))

	require.Len(t, ra.SealedCredentials, 1, "provider a must receive only its own sealed credential")
	assert.Equal(t, "k_a", ra.SealedCredentials[0].AudienceKID)
	require.Len(t, rb.SealedCredentials, 1, "provider b must receive only its own sealed credential")
	assert.Equal(t, "k_b", rb.SealedCredentials[0].AudienceKID)

	// Signatures verify against each provider's routed subset.
	require.NoError(t, tmproto.VerifyIdentityMatch(&ra, provA.URL, ca.sig, ca.kid, ks, time.Now()))
	require.NoError(t, tmproto.VerifyIdentityMatch(&rb, provB.URL, cb.sig, cb.kid, ks, time.Now()))
}

// A provider that declares no audience keys never receives sealed credentials,
// even when the request carries them.
func TestRouter_IdentityMatch_NoAudienceKeysGetsNoSealedCredentials(t *testing.T) {
	var cap atomic.Value
	prov := mkIdentityProvider(&cap)
	defer prov.Close()

	router, _, _ := newSignedTestRouter(t, []ProviderConfig{
		{ID: "p", Endpoint: prov.URL, IdentityMatch: true, Countries: []string{"US"}, UIDTypes: []string{"uid2"}, Timeout: 5 * time.Second},
	})

	body := `{
		"type":"identity_match_request",
		"request_id":"id-att",
		"seller_agent_url":"https://seller.example.com/agent",
		"identities":[{"user_token":"tok","uid_type":"uid2"}],
		"sealed_credentials":[{"audience_kid":"k_a","payload":"k_a.AAAA"}],
		"country":"US"
	}`
	w := httptest.NewRecorder()
	router.HandleIdentityMatch(w, httptest.NewRequest("POST", "/tmp/identity", strings.NewReader(body)))
	require.Equal(t, 200, w.Code, w.Body.String())

	c, ok := cap.Load().(identityCapture)
	require.True(t, ok, "provider not called")
	var parsed tmproto.IdentityMatchRequest
	require.NoError(t, json.Unmarshal(c.body, &parsed))
	assert.Empty(t, parsed.SealedCredentials, "provider with no audience keys must receive no sealed credentials")
}
