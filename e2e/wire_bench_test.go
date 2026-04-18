package e2e

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// TestWire_JSONCost measures the pure JSON marshal/unmarshal cost
// for the TMP wire format, isolated from HTTP and engine evaluation.
func TestWire_JSONCost(t *testing.T) {
	ctxReq := tmproto.ContextMatchRequest{
		ProtocolVersion: "1.0",
		RequestID:       "bench-ctx-001",
		PropertyID:      "pub-oakwood",
		PropertyRID:     "rid-pub-oakwood",
		PropertyType:    tmproto.PropertyTypeWebsite,
		PlacementID:     "sidebar-300x250",
		Geo:             map[string]any{"country": "US", "region": "NY"},
		PackageIDs:      []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe"},
	}

	idReq := tmproto.IdentityMatchRequest{
		ProtocolVersion: "1.0",
		RequestID:       "bench-id-001",
		Identities:      []tmproto.IdentityToken{{UserToken: "tok_uid2_example_not_a_real_token", UIDType: tmproto.UIDTypeUID2}},
		PackageIDs:      []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe", "pkg-auto-video", "pkg-travel-sponsored", "pkg-pharma-awareness"},
	}

	brandJSON, _ := json.Marshal(map[string]string{"name": "Meridian Foods"})
	ctxResp := tmproto.ContextMatchResponse{
		RequestID: "bench-ctx-001",
		Offers: []tmproto.Offer{
			{PackageID: "pkg-food-display", Brand: json.RawMessage(brandJSON), Price: tmproto.OfferPrice{Amount: 12.50, Currency: "USD", Model: string(tmproto.PriceModelCPM)}, Summary: "Olive oil sponsored"},
			{PackageID: "pkg-family-safe"},
		},
		Signals: map[string]any{"segments": []string{"food", "cooking"}},
	}

	idResp := tmproto.IdentityMatchResponse{
		RequestID:          "bench-id-001",
		EligiblePackageIDs: []string{"pkg-food-display", "pkg-tech-native", "pkg-family-safe"},
		TTLSec:             300,
	}

	const iterations = 100_000

	// Measure marshal cost.
	marshalStart := testing.AllocsPerRun(iterations, func() {
		json.Marshal(ctxReq)
		json.Marshal(idReq)
		json.Marshal(ctxResp)
		json.Marshal(idResp)
	})

	t.Logf("")
	t.Logf("=== JSON Wire Cost (per full request cycle) ===")
	t.Logf("")

	// Marshal.
	var totalMarshalBytes int
	ctxReqBytes, _ := json.Marshal(ctxReq)
	idReqBytes, _ := json.Marshal(idReq)
	ctxRespBytes, _ := json.Marshal(ctxResp)
	idRespBytes, _ := json.Marshal(idResp)
	totalMarshalBytes = len(ctxReqBytes) + len(idReqBytes) + len(ctxRespBytes) + len(idRespBytes)

	t.Logf("  Payloads: %d bytes total", totalMarshalBytes)
	t.Logf("    Context request:  %d bytes", len(ctxReqBytes))
	t.Logf("    Identity request: %d bytes", len(idReqBytes))
	t.Logf("    Context response: %d bytes", len(ctxRespBytes))
	t.Logf("    Identity response: %d bytes", len(idRespBytes))
	t.Logf("  Marshal allocs/cycle: %.0f", marshalStart)
	t.Logf("")

	// Timed marshal.
	start := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			json.Marshal(ctxReq)
			json.Marshal(idReq)
			json.Marshal(ctxResp)
			json.Marshal(idResp)
		}
	})
	marshalNs := start.NsPerOp()

	// Timed unmarshal.
	unmarshal := testing.Benchmark(func(b *testing.B) {
		var cr tmproto.ContextMatchRequest
		var ir tmproto.IdentityMatchRequest
		var crs tmproto.ContextMatchResponse
		var irs tmproto.IdentityMatchResponse
		for range b.N {
			json.Unmarshal(ctxReqBytes, &cr)
			json.Unmarshal(idReqBytes, &ir)
			json.Unmarshal(ctxRespBytes, &crs)
			json.Unmarshal(idRespBytes, &irs)
		}
	})
	unmarshalNs := unmarshal.NsPerOp()

	// Simulated router overhead: router does unmarshal+marshal on the request path.
	routerOverhead := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			// Router receives request, unmarshals, re-marshals for fan-out.
			var cmReq tmproto.ContextMatchRequest
			json.Unmarshal(ctxReqBytes, &cmReq)
			json.Marshal(&cmReq)

			var imReq tmproto.IdentityMatchRequest
			json.Unmarshal(idReqBytes, &imReq)
			json.Marshal(&imReq)

			// Router receives responses, unmarshals to merge.
			var cmResp tmproto.ContextMatchResponse
			json.Unmarshal(ctxRespBytes, &cmResp)
			var imResp tmproto.IdentityMatchResponse
			json.Unmarshal(idRespBytes, &imResp)

			// Router marshals merged response back.
			json.Marshal(&cmResp)
			json.Marshal(&imResp)
		}
	})
	routerOverheadNs := routerOverhead.NsPerOp()

	// Full cycle: client marshal → router unmarshal+marshal → agent unmarshal+marshal → router unmarshal+marshal → client unmarshal
	fullCycle := testing.Benchmark(func(b *testing.B) {
		for range b.N {
			// Client marshals requests.
			ctxBytes, _ := json.Marshal(ctxReq)
			idBytes, _ := json.Marshal(idReq)

			// Router unmarshals requests.
			var cmReq tmproto.ContextMatchRequest
			json.Unmarshal(ctxBytes, &cmReq)
			var imReq tmproto.IdentityMatchRequest
			json.Unmarshal(idBytes, &imReq)

			// Router re-marshals for agent fan-out.
			ctxFanout, _ := json.Marshal(&cmReq)
			idFanout, _ := json.Marshal(&imReq)

			// Agent unmarshals.
			var agentCtx tmproto.ContextMatchRequest
			json.Unmarshal(ctxFanout, &agentCtx)
			var agentId tmproto.IdentityMatchRequest
			json.Unmarshal(idFanout, &agentId)

			// Agent marshals responses.
			ctxR, _ := json.Marshal(ctxResp)
			idR, _ := json.Marshal(idResp)

			// Router unmarshals responses.
			var routerCtxResp tmproto.ContextMatchResponse
			json.Unmarshal(ctxR, &routerCtxResp)
			var routerIdResp tmproto.IdentityMatchResponse
			json.Unmarshal(idR, &routerIdResp)

			// Router marshals merged response back to client.
			json.Marshal(&routerCtxResp)
			json.Marshal(&routerIdResp)

			// Client unmarshals.
			var clientCtx tmproto.ContextMatchResponse
			json.Unmarshal(ctxR, &clientCtx)
			var clientId tmproto.IdentityMatchResponse
			json.Unmarshal(idR, &clientId)

			_ = bytes.Compare(nil, nil) // prevent optimization
		}
	})
	fullCycleNs := fullCycle.NsPerOp()

	t.Logf("  Timing:")
	t.Logf("    4x Marshal (client+agent):    %6dns", marshalNs)
	t.Logf("    4x Unmarshal (router+client):  %6dns", unmarshalNs)
	t.Logf("    Router overhead (re-ser):      %6dns", routerOverheadNs)
	t.Logf("    Full cycle (all 14 JSON ops):  %6dns", fullCycleNs)
	t.Logf("")
	t.Logf("  Full cycle = %.1fµs of JSON per request", float64(fullCycleNs)/1000)
	t.Logf("  vs ~113µs total for resolved engine eval")
	t.Logf("  JSON is %.0f%% of total request time (engine + wire)", float64(fullCycleNs)/float64(fullCycleNs+113000)*100)
	t.Logf("")
}
