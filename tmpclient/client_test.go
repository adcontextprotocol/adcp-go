package tmpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextMatch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/tmp/context", r.URL.Path, "unexpected path")
		assert.Equal(t, http.MethodPost, r.Method, "expected POST")

		var req tmproto.ContextMatchRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		resp := tmproto.ContextMatchResponse{
			RequestID: req.RequestID,
			Offers:    []tmproto.Offer{{PackageID: "pkg-1"}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.ContextMatch(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "ctx-1",
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "top-banner",
		PackageIDs: []string{"pkg-1"},
	})
	require.NoError(t, err)
	require.Len(t, resp.Offers, 1)
	assert.Equal(t, "pkg-1", resp.Offers[0].PackageID)
}

func TestIdentityMatch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmproto.IdentityMatchRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)

		resp := tmproto.IdentityMatchResponse{
			RequestID:          req.RequestID,
			EligiblePackageIDs: []string{"pkg-1"},
			TTLSec:             300,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.IdentityMatch(context.Background(), &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		UserToken:  "tok-abc",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-1"},
	})
	require.NoError(t, err)
	require.Len(t, resp.EligiblePackageIDs, 1)
	assert.Equal(t, "pkg-1", resp.EligiblePackageIDs[0])
	assert.Equal(t, 300, resp.TTLSec)
}

func TestContextMatch_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{
			RequestID: "err-1",
			Code:      tmproto.ErrorCodeInvalidRequest,
			Message:   "missing field",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ContextMatch(context.Background(), &tmproto.ContextMatchRequest{
		RequestID:    "err-1",
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs: []string{"pkg-1"},
	})
	require.Error(t, err)
	var tmpErr *TMPError
	require.True(t, errors.As(err, &tmpErr), "expected TMPError, got %T: %v", err, err)
	assert.Equal(t, tmproto.ErrorCodeInvalidRequest, tmpErr.Code)
}

func TestContextMatch_ValidationFailure(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ContextMatch(context.Background(), &tmproto.ContextMatchRequest{
		// Missing required fields.
	})
	require.Error(t, err, "expected validation error")
	assert.False(t, called.Load(), "server should not be called on validation failure")
}

func TestContextMatch_AutoGeneratesRequestID(t *testing.T) {
	var receivedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmproto.ContextMatchRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		receivedID = req.RequestID
		resp := tmproto.ContextMatchResponse{RequestID: req.RequestID}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ContextMatch(context.Background(), &tmproto.ContextMatchRequest{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs: []string{"pkg-1"},
		// RequestID intentionally empty.
	})
	require.NoError(t, err)
	assert.NotEmpty(t, receivedID, "expected auto-generated request ID")
}

func TestActivate_HappyPath(t *testing.T) {
	var contextCalled, identityCalled atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			contextCalled.Store(true)
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				RequestID: "ctx-1",
				Offers: []tmproto.Offer{
					{PackageID: "pkg-food"},
					{PackageID: "pkg-tech"},
				},
				Signals: map[string]any{"segments": []any{"cooking"}},
			})
		case "/tmp/identity":
			identityCalled.Store(true)
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				RequestID:          "id-1",
				EligiblePackageIDs: []string{"pkg-food"},
				TTLSec:             60,
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-food", "pkg-tech"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err)

	assert.True(t, contextCalled.Load(), "context endpoint should be called")
	assert.True(t, identityCalled.Load(), "identity endpoint should be called")

	require.Len(t, result.Activations, 1)

	a := result.Activations[0]
	assert.Equal(t, "pkg-food", a.PackageID)
	require.NotNil(t, result.Signals)
	segs, ok := result.Signals["segments"].([]any)
	require.True(t, ok, "segments should be []any, got %T", result.Signals["segments"])
	assert.Len(t, segs, 1)
}

func TestActivate_NoOverlap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				Offers: []tmproto.Offer{{PackageID: "pkg-a"}},
			})
		case "/tmp/identity":
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				EligiblePackageIDs: []string{"pkg-b"},
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-a", "pkg-b"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Activations, "expected 0 activations (no overlap)")
}

func TestActivate_ContextFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInternalError})
		case "/tmp/identity":
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-1"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
	})
	assert.Error(t, err, "expected error when context match fails")
}

func TestActivate_MultiPackage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				Offers: []tmproto.Offer{
					{PackageID: "pkg-a"},
					{PackageID: "pkg-b"},
					{PackageID: "pkg-c"},
				},
			})
		case "/tmp/identity":
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				EligiblePackageIDs: []string{"pkg-a", "pkg-b", "pkg-c"},
				TTLSec:             120,
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-a", "pkg-b", "pkg-c"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err)

	require.Len(t, result.Activations, 3)
	// Activations should follow context offer order.
	assert.Equal(t, "pkg-a", result.Activations[0].PackageID, "expected pkg-a first")
	assert.Equal(t, "pkg-b", result.Activations[1].PackageID, "expected pkg-b second")
	assert.Equal(t, "pkg-c", result.Activations[2].PackageID, "expected pkg-c third")
}

func TestActivate_PackageIDsSentToBothEndpoints(t *testing.T) {
	var receivedPkgIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{})
		case "/tmp/identity":
			var req tmproto.IdentityMatchRequest
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			receivedPkgIDs = req.PackageIDs
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-a", "pkg-b"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"pkg-a", "pkg-b"}, receivedPkgIDs, "expected [pkg-a pkg-b]")
}

func TestActivate_Tmpx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				Offers: []tmproto.Offer{{PackageID: "pkg-1"}},
			})
		case "/tmp/identity":
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				EligiblePackageIDs: []string{"pkg-1"},
				Tmpx:               "k1.dGVzdC10bXB4LXRva2Vu",
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-1"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		Country:      "US",
	})
	require.NoError(t, err)
	require.Len(t, result.Activations, 1)
	assert.Equal(t, "k1.dGVzdC10bXB4LXRva2Vu", result.Tmpx)
}

func TestActivate_CountryPassedThrough(t *testing.T) {
	var receivedCountry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			_ = json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{})
		case "/tmp/identity":
			var req tmproto.IdentityMatchRequest
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			receivedCountry = req.Country
			_ = json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		PackageIDs:   []string{"pkg-1"},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		Country:      "DE",
	})
	require.NoError(t, err)
	assert.Equal(t, "DE", receivedCountry, "expected country DE sent to router")
}

func TestJoinResults_Empty(t *testing.T) {
	result := joinResults(
		&tmproto.ContextMatchResponse{},
		&tmproto.IdentityMatchResponse{},
	)
	assert.Empty(t, result, "expected 0 activations")
}
