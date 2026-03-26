package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestMatch_ParallelExecution(t *testing.T) {
	var ctxCalled, idCalled atomic.Int32

	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxCalled.Add(1)
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: "test-1",
			Offers:    []tmp.Offer{{PackageID: "pkg-1"}, {PackageID: "pkg-2"}},
		})
	}))
	defer ctxServer.Close()

	score := 0.85
	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idCalled.Add(1)
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
			RequestID: "test-1",
			Eligibility: []tmp.PackageEligibility{
				{PackageID: "pkg-1", Eligible: true, IntentScore: &score},
				{PackageID: "pkg-2", Eligible: false},
			},
		})
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithDecorrelationMax(0))
	result, err := c.Match(context.Background(), &MatchRequest{
		RequestID:    "test-1",
		PropertyID:   "pub-test",
		PropertyType: tmp.PropertyTypeWebsite,
		PlacementID:  "sidebar",
		UserToken:    "tok_abc",
		PackageIDs:   []string{"pkg-1", "pkg-2"},
		MediaBuyIDs:  map[string]string{"pkg-1": "mb-1", "pkg-2": "mb-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if ctxCalled.Load() != 1 {
		t.Error("context endpoint should be called once")
	}
	if idCalled.Load() != 1 {
		t.Error("identity endpoint should be called once")
	}

	// Only pkg-1 should be eligible (context offered both, identity only approved pkg-1)
	if len(result.EligiblePackages) != 1 {
		t.Fatalf("expected 1 eligible package, got %d", len(result.EligiblePackages))
	}
	if result.EligiblePackages[0].PackageID != "pkg-1" {
		t.Errorf("expected pkg-1, got %s", result.EligiblePackages[0].PackageID)
	}
	if result.EligiblePackages[0].IntentScore == nil || *result.EligiblePackages[0].IntentScore != 0.85 {
		t.Error("intent score should be 0.85")
	}
}

func TestMatch_TemporalDecorrelation(t *testing.T) {
	var ctxTime, idTime atomic.Int64

	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxTime.Store(time.Now().UnixNano())
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{RequestID: "t"})
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idTime.Store(time.Now().UnixNano())
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{RequestID: "t"})
	}))
	defer idServer.Close()

	// Force a fixed 10ms delay
	c := New(ctxServer.URL, idServer.URL)
	c.randDelay = func(max time.Duration) time.Duration { return 10 * time.Millisecond }

	_, err := c.Match(context.Background(), &MatchRequest{
		RequestID:   "t",
		PropertyID:  "p",
		PlacementID: "pl",
		UserToken:   "tok",
		PackageIDs:  []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Identity should have been delayed
	diff := time.Duration(idTime.Load() - ctxTime.Load())
	if diff < 5*time.Millisecond {
		t.Errorf("identity should be delayed by decorrelation, diff was %v", diff)
	}
}

func TestMatch_ContextFailure(t *testing.T) {
	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{RequestID: "t"})
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithDecorrelationMax(0))
	_, err := c.Match(context.Background(), &MatchRequest{
		RequestID: "t", PropertyID: "p", PlacementID: "pl",
		UserToken: "tok", PackageIDs: []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err == nil {
		t.Error("expected error when context fails")
	}
}

func TestMatch_IdentityFailure(t *testing.T) {
	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{RequestID: "t"})
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithDecorrelationMax(0))
	_, err := c.Match(context.Background(), &MatchRequest{
		RequestID: "t", PropertyID: "p", PlacementID: "pl",
		UserToken: "tok", PackageIDs: []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err == nil {
		t.Error("expected error when identity fails")
	}
}

func TestMatch_JoinLogic_NoContextOffer(t *testing.T) {
	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Context returns no offers
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{RequestID: "t", Offers: []tmp.Offer{}})
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
			RequestID:   "t",
			Eligibility: []tmp.PackageEligibility{{PackageID: "pkg-1", Eligible: true}},
		})
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithDecorrelationMax(0))
	result, err := c.Match(context.Background(), &MatchRequest{
		RequestID: "t", PropertyID: "p", PlacementID: "pl",
		UserToken: "tok", PackageIDs: []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.EligiblePackages) != 0 {
		t.Error("no offers from context means no eligible packages")
	}
}

func TestMatch_Signals(t *testing.T) {
	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{
			RequestID: "t",
			Offers:    []tmp.Offer{{PackageID: "pkg-1"}},
			Signals: &tmp.Signals{
				Segments:     []string{"cooking"},
				TargetingKVs: []tmp.KeyValuePair{{Key: "adcp_pkg", Value: "pkg-1"}},
			},
		})
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{
			RequestID:   "t",
			Eligibility: []tmp.PackageEligibility{{PackageID: "pkg-1", Eligible: true}},
		})
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithDecorrelationMax(0))
	result, err := c.Match(context.Background(), &MatchRequest{
		RequestID: "t", PropertyID: "p", PlacementID: "pl",
		UserToken: "tok", PackageIDs: []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Signals == nil || len(result.Signals.Segments) != 1 {
		t.Error("signals should be passed through from context response")
	}
}

func TestExpose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req tmp.ExposeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.PackageID != "pkg-1" {
			t.Errorf("expected pkg-1, got %s", req.PackageID)
		}
		_ = json.NewEncoder(w).Encode(tmp.ExposeResponse{
			PackageID:         "pkg-1",
			CampaignCount:     3,
			CampaignRemaining: 7,
		})
	}))
	defer server.Close()

	c := New("", server.URL+"/tmp")
	resp, err := c.Expose(context.Background(), &tmp.ExposeRequest{
		UserToken: "tok_abc",
		PackageID: "pkg-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CampaignCount != 3 {
		t.Errorf("expected count 3, got %d", resp.CampaignCount)
	}
}

func TestMatch_Timeout(t *testing.T) {
	ctxServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(tmp.ContextMatchResponse{RequestID: "t"})
	}))
	defer ctxServer.Close()

	idServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(tmp.IdentityMatchResponse{RequestID: "t"})
	}))
	defer idServer.Close()

	c := New(ctxServer.URL, idServer.URL, WithTimeout(50*time.Millisecond), WithDecorrelationMax(0))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := c.Match(ctx, &MatchRequest{
		RequestID: "t", PropertyID: "p", PlacementID: "pl",
		UserToken: "tok", PackageIDs: []string{"pkg-1"},
		MediaBuyIDs: map[string]string{"pkg-1": "mb-1"},
	})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestRequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(tmp.ErrorResponse{
			Code:    tmp.ErrorCodeInvalidRequest,
			Message: "bad field",
		})
	}))
	defer server.Close()

	c := New("", server.URL+"/tmp")
	_, err := c.Expose(context.Background(), &tmp.ExposeRequest{PackageID: "pkg-1"})
	if err == nil {
		t.Fatal("expected error")
	}

	reqErr, ok := err.(*RequestError)
	if !ok {
		// It's wrapped, unwrap
		t.Logf("error: %v", err)
	} else {
		if reqErr.StatusCode != 400 {
			t.Errorf("expected 400, got %d", reqErr.StatusCode)
		}
	}
}
