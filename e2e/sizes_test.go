package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/targeting"
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestMeasure_RequestResponseSizes(t *testing.T) {
	// Context match request (what a publisher sends).
	ctxReq := tmproto.ContextMatchRequest{
		ProtocolVersion: "1.0",
		RequestID:       "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		PropertyID:      "pub-oakwood",
		PropertyRID:     "rid-pub-oakwood",
		PropertyType:    tmproto.PropertyTypeWebsite,
		PlacementID:     "sidebar-300x250",
		Geo:             map[string]any{"country": "US", "region": "NY"},
		PackageIDs:      []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe"},
	}

	// Identity match request.
	idReq := tmproto.IdentityMatchRequest{
		ProtocolVersion: "1.0",
		RequestID:       "f9e8d7c6-b5a4-3210-fedc-ba0987654321",
		Identities:      []tmproto.IdentityToken{{UserToken: "tok_uid2_example_not_a_real_token", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs:      []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe", "pkg-auto-video", "pkg-travel-sponsored", "pkg-pharma-awareness"},
	}

	// Context response (what comes back).
	ctxResp := tmproto.ContextMatchResponse{
		RequestID: ctxReq.RequestID,
		Offers: []tmproto.Offer{
			{PackageID: "pkg-food-display", Summary: "Meridian olive oil sponsored content"},
			{PackageID: "pkg-family-safe"},
		},
		Signals: map[string]any{
			"segments": []string{"food", "cooking", "recipes", "lifestyle"},
			"topic":    "food.italian",
			"brand_ok": "true",
		},
	}

	// Identity response.
	idResp := tmproto.IdentityMatchResponse{
		RequestID:          idReq.RequestID,
		EligiblePackageIDs: []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe", "pkg-travel-sponsored"},
		TTLSec:             300,
	}

	// Expose request (local types — not part of tmproto wire format).
	expReq := map[string]any{
		"user_token":  "tok_uid2_example_not_a_real_token",
		"uid_type":    "uid2",
		"package_id":  "pkg-food-display",
		"campaign_id": "campaign-acme-q1",
		"timestamp":   time.Now().Unix(),
	}

	expResp := map[string]any{
		"package_id":         "pkg-food-display",
		"campaign_count":     3,
		"campaign_remaining": 7,
	}

	sizes := []struct {
		name string
		v    any
	}{
		{"Context Match Request", ctxReq},
		{"Context Match Response", ctxResp},
		{"Identity Match Request", idReq},
		{"Identity Match Response", idResp},
		{"Expose Request", expReq},
		{"Expose Response", expResp},
	}

	t.Log("")
	t.Log("=== TMP Wire Sizes (JSON) ===")
	t.Log("")
	totalReq := 0
	totalResp := 0
	for _, s := range sizes {
		data, _ := json.Marshal(s.v)
		t.Logf("  %-30s %4d bytes", s.name, len(data))
		if s.name == "Context Match Request" || s.name == "Identity Match Request" || s.name == "Expose Request" {
			totalReq += len(data)
		} else {
			totalResp += len(data)
		}
	}
	t.Log("")
	t.Logf("  Total request payload:  %d bytes", totalReq)
	t.Logf("  Total response payload: %d bytes", totalResp)
	t.Logf("  Full round-trip:        %d bytes", totalReq+totalResp)
	t.Log("")

	// Compare with OpenRTB equivalent.
	ortbReq := `{"id":"auction-123","imp":[{"id":"imp-1","banner":{"w":300,"h":250,"format":[{"w":300,"h":250},{"w":320,"h":50}]},"bidfloor":0.5,"bidfloorcur":"USD","ext":{"skadn":{"versions":["2.0","3.0"]}}}],"site":{"domain":"oakwoodpublishing.example.com","page":"https://oakwoodpublishing.example.com/recipes/pasta-carbonara","cat":["IAB8","IAB8-5","IAB8-18"],"publisher":{"id":"pub-12345","name":"Oakwood Publishing","domain":"oakwoodpublishing.example.com"}},"device":{"ua":"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X)","ip":"203.0.113.42","geo":{"lat":40.7128,"lon":-74.006,"type":2,"country":"USA","region":"NY","metro":"501","city":"New York","zip":"10001"},"devicetype":4,"make":"Apple","model":"iPhone","os":"iOS","osv":"17.4","language":"en","carrier":"AT&T","connectiontype":6,"ifa":"6D92078A-8246-4BA4-AE5B-76104861E7DC"},"user":{"id":"user-hash-abc123","buyeruid":"buyer-uid-xyz","ext":{"eids":[{"source":"uidapi.com","uids":[{"id":"uid2-example-not-real","atype":3}]}]}},"at":1,"tmax":100,"cur":["USD"],"bcat":["IAB25","IAB26"],"badv":["competitor.com"]}`

	ortbResp := `{"id":"auction-123","seatbid":[{"bid":[{"id":"bid-abc-001","impid":"imp-1","price":2.35,"adid":"creative-12345","nurl":"https://tracker.example.com/win?price=${AUCTION_PRICE}","adm":"<div class=\"ad\"><a href=\"https://advertiser.example.com/landing?utm_source=oakwood&utm_medium=display\"><img src=\"https://cdn.example.com/creative/olive-oil-300x250.jpg\" width=\"300\" height=\"250\" alt=\"Meridian Olive Oil\"/></a><img src=\"https://tracker.example.com/imp?id=bid-abc-001\" width=\"1\" height=\"1\"/></div>","adomain":["meridian-foods.example.com"],"crid":"creative-12345","w":300,"h":250,"cat":["IAB8"]}],"seat":"seat-meridian"}],"cur":"USD"}`

	t.Log("=== Comparison with OpenRTB ===")
	t.Log("")
	t.Logf("  OpenRTB BidRequest:     %4d bytes", len(ortbReq))
	t.Logf("  OpenRTB BidResponse:    %4d bytes", len(ortbResp))
	t.Logf("  OpenRTB round-trip:     %4d bytes", len(ortbReq)+len(ortbResp))
	t.Log("")

	tmpTotal := totalReq + totalResp
	ortbTotal := len(ortbReq) + len(ortbResp)
	reduction := float64(ortbTotal-tmpTotal) / float64(ortbTotal) * 100
	t.Logf("  TMP vs OpenRTB: %.0f%% smaller (%d vs %d bytes)", reduction, tmpTotal, ortbTotal)
	t.Log("")
}

func TestMeasure_TargetingScale(t *testing.T) {
	t.Log("")
	t.Log("=== Integration Test Targeting Scale ===")
	t.Log("")
	t.Log("  Properties in bitmap:     5 (global)")
	t.Log("  Packages configured:      3 (context) + 3 (identity)")
	t.Log("  Campaigns:                1 (campaign-acme)")
	t.Log("  Audience segments:        2 (cooking_fans, sports_fans)")
	t.Log("  Topic sets:               2 packages × 2-3 topics each")
	t.Log("  URL blocklist entries:    1 (pkg-family)")
	t.Log("  Frequency rules:          2 (pkg-food: 3/24h, pkg-family: 5/7d)")
	t.Log("  Campaign frequency:       1 (campaign-acme: 5/7d)")
	t.Log("")

	// Now measure what production scale looks like.
	t.Log("=== Production-Scale Bitmap Performance ===")
	t.Log("")

	for _, n := range []int{1000, 10_000, 50_000, 100_000} {
		bm := make(targeting.MapBitmap, n)
		for i := range n {
			bm[fmt.Sprintf("rid-%d", i*7)] = struct{}{} // sparse distribution
		}
		// Measure lookup time.
		start := time.Now()
		const lookups = 1_000_000
		hits := 0
		for i := range lookups {
			if bm.Contains(fmt.Sprintf("rid-%d", i%(n*10))) {
				hits++
			}
		}
		elapsed := time.Since(start)
		t.Logf("  %6d properties: %v per lookup (%d/%d hits, MapBitmap)",
			n, elapsed/lookups, hits, lookups)
	}
	t.Log("")

	// Measure store operations at scale.
	t.Log("=== MockStore Set Membership at Scale ===")
	t.Log("")
	store := targeting.NewMockStore()

	for _, n := range []int{100, 1000, 10_000, 100_000} {
		key := fmt.Sprintf("audience:seg-%d", n)
		for i := range n {
			store.SetAdd(key, fmt.Sprintf("token-hash-%d", i))
		}
		start := time.Now()
		const lookups = 100_000
		for i := range lookups {
			store.SetIsMember(nil, key, fmt.Sprintf("token-hash-%d", i%n))
		}
		elapsed := time.Since(start)
		t.Logf("  %6d members: %v per lookup", n, elapsed/lookups)
	}
	t.Log("")
}
