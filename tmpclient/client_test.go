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
)

func TestContextMatch_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tmp/context" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

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
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1", MediaBuyID: "mb-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Offers) != 1 || resp.Offers[0].PackageID != "pkg-1" {
		t.Errorf("unexpected offers: %+v", resp.Offers)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.EligiblePackageIDs) != 1 || resp.EligiblePackageIDs[0] != "pkg-1" {
		t.Errorf("unexpected eligible IDs: %+v", resp.EligiblePackageIDs)
	}
	if resp.TTLSec != 300 {
		t.Errorf("expected TTLSec 300, got %d", resp.TTLSec)
	}
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
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var tmpErr *TMPError
	if !errors.As(err, &tmpErr) {
		t.Fatalf("expected TMPError, got %T: %v", err, err)
	}
	if tmpErr.Code != tmproto.ErrorCodeInvalidRequest {
		t.Errorf("expected code invalid_request, got %s", tmpErr.Code)
	}
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
	if err == nil {
		t.Fatal("expected validation error")
	}
	if called.Load() {
		t.Error("server should not be called on validation failure")
	}
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
		AvailablePkgs: []tmproto.AvailablePackage{
			{PackageID: "pkg-1"},
		},
		// RequestID intentionally empty.
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedID == "" {
		t.Error("expected auto-generated request ID")
	}
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
				Signals: &tmproto.Signals{Segments: []string{"cooking"}},
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
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-food"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-tech"},
		},
		UserToken:  "tok-abc",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-food", "pkg-tech"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !contextCalled.Load() || !identityCalled.Load() {
		t.Error("both endpoints should be called")
	}

	if len(result.Activations) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.Activations))
	}

	a := result.Activations[0]
	if a.PackageID != "pkg-food" {
		t.Errorf("expected pkg-food, got %s", a.PackageID)
	}
	if a.MediaBuyID != "mb-food" {
		t.Errorf("expected mb-food, got %s", a.MediaBuyID)
	}
	if result.Signals == nil || len(result.Signals.Segments) != 1 {
		t.Error("expected signals with 1 segment")
	}
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
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-a"}},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Activations) != 0 {
		t.Errorf("expected 0 activations (no overlap), got %d", len(result.Activations))
	}
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
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-1"},
	})
	if err == nil {
		t.Error("expected error when context match fails")
	}
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
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-a", MediaBuyID: "mb-a"},
			{PackageID: "pkg-b", MediaBuyID: "mb-b"},
			{PackageID: "pkg-c", MediaBuyID: "mb-c"},
		},
		UserToken:  "tok-abc",
		UIDType:    tmproto.UIDTypeUID2,
		PackageIDs: []string{"pkg-a", "pkg-b", "pkg-c"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Activations) != 3 {
		t.Fatalf("expected 3, got %d", len(result.Activations))
	}
	// Activations should follow context offer order.
	if result.Activations[0].PackageID != "pkg-a" {
		t.Errorf("expected pkg-a first, got %s", result.Activations[0].PackageID)
	}
	if result.Activations[1].PackageID != "pkg-b" {
		t.Errorf("expected pkg-b second, got %s", result.Activations[1].PackageID)
	}
	if result.Activations[2].PackageID != "pkg-c" {
		t.Errorf("expected pkg-c third, got %s", result.Activations[2].PackageID)
	}
}

func TestActivate_DerivesPackageIDs(t *testing.T) {
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
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-a"},
			{PackageID: "pkg-b"},
		},
		UserToken: "tok-abc",
		UIDType:   tmproto.UIDTypeUID2,
		// PackageIDs not set — should derive from Packages.
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receivedPkgIDs) != 2 || receivedPkgIDs[0] != "pkg-a" || receivedPkgIDs[1] != "pkg-b" {
		t.Errorf("expected derived [pkg-a pkg-b], got %v", receivedPkgIDs)
	}
}

func TestActivate_TmpxProviders(t *testing.T) {
	// Mock acts as router — returns tmpx_providers map (as router would after merge).
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
				TmpxProviders: map[string]string{
					"scope3": "k1.dGVzdC10bXB4LXRva2Vu",
				},
			})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	result, err := c.Activate(context.Background(), &ActivateParams{
		PropertyID:   "prop-1",
		PropertyType: tmproto.PropertyTypeWebsite,
		PlacementID:  "banner",
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-1"},
		Country:      "US",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Activations) != 1 {
		t.Fatalf("expected 1 activation, got %d", len(result.Activations))
	}
	if result.TmpxProviders["scope3"] != "k1.dGVzdC10bXB4LXRva2Vu" {
		t.Errorf("expected TMPX for scope3, got %v", result.TmpxProviders)
	}
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
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-1"}},
		UserToken:    "tok-abc",
		UIDType:      tmproto.UIDTypeUID2,
		PackageIDs:   []string{"pkg-1"},
		Country:      "DE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receivedCountry != "DE" {
		t.Errorf("expected country DE sent to router, got %q", receivedCountry)
	}
}

func TestJoinResults_Empty(t *testing.T) {
	result := joinResults(
		&tmproto.ContextMatchResponse{},
		&tmproto.IdentityMatchResponse{},
		nil,
	)
	if len(result) != 0 {
		t.Errorf("expected 0 activations, got %d", len(result))
	}
}
