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
		json.NewEncoder(w).Encode(resp)
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
			RequestID: req.RequestID,
			Eligibility: []tmproto.PackageEligibility{
				{PackageID: "pkg-1", Eligible: true},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.IdentityMatch(context.Background(), &tmproto.IdentityMatchRequest{
		RequestID:  "id-1",
		UserToken:  "tok-abc",
		PackageIDs: []string{"pkg-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Eligibility) != 1 || !resp.Eligibility[0].Eligible {
		t.Errorf("unexpected eligibility: %+v", resp.Eligibility)
	}
}

func TestExpose_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tmp/expose" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := tmproto.ExposeResponse{PackageID: "pkg-1", CampaignCount: 3, CampaignRemaining: 2}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	resp, err := c.Expose(context.Background(), &tmproto.ExposeRequest{
		UserToken: "tok-abc",
		PackageID: "pkg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CampaignCount != 3 {
		t.Errorf("expected count 3, got %d", resp.CampaignCount)
	}
}

func TestContextMatch_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(tmproto.ErrorResponse{
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
		json.NewEncoder(w).Encode(resp)
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
			json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				RequestID: "ctx-1",
				Offers: []tmproto.Offer{
					{PackageID: "pkg-food"},
					{PackageID: "pkg-tech"},
				},
				Signals: &tmproto.Signals{Segments: []string{"cooking"}},
			})
		case "/tmp/identity":
			identityCalled.Store(true)
			score := 0.95
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				RequestID: "id-1",
				Eligibility: []tmproto.PackageEligibility{
					{PackageID: "pkg-food", Eligible: true, IntentScore: &score},
					{PackageID: "pkg-tech", Eligible: false},
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
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-food", MediaBuyID: "mb-food"},
			{PackageID: "pkg-tech", MediaBuyID: "mb-tech"},
		},
		UserToken:  "tok-abc",
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
	if a.IntentScore == nil || *a.IntentScore != 0.95 {
		t.Errorf("expected intent score 0.95, got %v", a.IntentScore)
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
			json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				Offers: []tmproto.Offer{{PackageID: "pkg-a"}},
			})
		case "/tmp/identity":
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				Eligibility: []tmproto.PackageEligibility{
					{PackageID: "pkg-b", Eligible: true},
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
		Packages:     []tmproto.AvailablePackage{{PackageID: "pkg-a"}},
		UserToken:    "tok-abc",
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
			json.NewEncoder(w).Encode(tmproto.ErrorResponse{Code: tmproto.ErrorCodeInternalError})
		case "/tmp/identity":
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{})
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
		PackageIDs:   []string{"pkg-1"},
	})
	if err == nil {
		t.Error("expected error when context match fails")
	}
}

func TestActivate_IntentScoreSorting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{
				Offers: []tmproto.Offer{
					{PackageID: "pkg-low"},
					{PackageID: "pkg-high"},
					{PackageID: "pkg-nil"},
				},
			})
		case "/tmp/identity":
			low := 0.3
			high := 0.9
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{
				Eligibility: []tmproto.PackageEligibility{
					{PackageID: "pkg-low", Eligible: true, IntentScore: &low},
					{PackageID: "pkg-high", Eligible: true, IntentScore: &high},
					{PackageID: "pkg-nil", Eligible: true},
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
		Packages: []tmproto.AvailablePackage{
			{PackageID: "pkg-low"},
			{PackageID: "pkg-high"},
			{PackageID: "pkg-nil"},
		},
		UserToken:  "tok-abc",
		PackageIDs: []string{"pkg-low", "pkg-high", "pkg-nil"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Activations) != 3 {
		t.Fatalf("expected 3, got %d", len(result.Activations))
	}
	if result.Activations[0].PackageID != "pkg-high" {
		t.Errorf("expected pkg-high first, got %s", result.Activations[0].PackageID)
	}
	if result.Activations[1].PackageID != "pkg-low" {
		t.Errorf("expected pkg-low second, got %s", result.Activations[1].PackageID)
	}
	if result.Activations[2].PackageID != "pkg-nil" {
		t.Errorf("expected pkg-nil last, got %s", result.Activations[2].PackageID)
	}
}

func TestActivate_DerivesPackageIDs(t *testing.T) {
	var receivedPkgIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/tmp/context":
			json.NewEncoder(w).Encode(tmproto.ContextMatchResponse{})
		case "/tmp/identity":
			var req tmproto.IdentityMatchRequest
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			receivedPkgIDs = req.PackageIDs
			json.NewEncoder(w).Encode(tmproto.IdentityMatchResponse{})
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
		// PackageIDs not set — should derive from Packages.
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receivedPkgIDs) != 2 || receivedPkgIDs[0] != "pkg-a" || receivedPkgIDs[1] != "pkg-b" {
		t.Errorf("expected derived [pkg-a pkg-b], got %v", receivedPkgIDs)
	}
}

func TestExpose_ValidationFailure(t *testing.T) {
	c := NewClient("http://unused")
	_, err := c.Expose(context.Background(), &tmproto.ExposeRequest{
		// Missing required fields.
	})
	if err == nil {
		t.Error("expected validation error")
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
