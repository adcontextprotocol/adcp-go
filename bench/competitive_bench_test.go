package bench

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Competitive benchmarks comparing TMP against OpenRTB. Run manually (not in CI).
//   go test -bench=. -benchmem ./bench/...
// See also competitive_capnp_test.go for TMP vs Cap'n Proto.

func TestPayloadSizes(t *testing.T) {
	ortbReq, _ := json.Marshal(RealisticOpenRTBRequest())
	ortbResp, _ := json.Marshal(RealisticOpenRTBResponse())
	tmpCtxReq, _ := json.Marshal(RealisticTMPContextRequest())
	tmpCtxResp, _ := json.Marshal(RealisticTMPContextResponse())
	tmpIdReq, _ := json.Marshal(RealisticTMPIdentityRequest())
	tmpIdResp, _ := json.Marshal(RealisticTMPIdentityResponse())

	fmt.Println("\n=== Payload Size Comparison ===")
	fmt.Println()
	fmt.Printf("  OpenRTB BidRequest:        %5d bytes\n", len(ortbReq))
	fmt.Printf("  OpenRTB BidResponse:       %5d bytes\n", len(ortbResp))
	fmt.Printf("  OpenRTB Total:             %5d bytes\n", len(ortbReq)+len(ortbResp))
	fmt.Println()
	fmt.Printf("  TMP ContextMatch Request:  %5d bytes\n", len(tmpCtxReq))
	fmt.Printf("  TMP ContextMatch Response: %5d bytes\n", len(tmpCtxResp))
	fmt.Printf("  TMP IdentityMatch Request: %5d bytes\n", len(tmpIdReq))
	fmt.Printf("  TMP IdentityMatch Response:%5d bytes\n", len(tmpIdResp))
	fmt.Printf("  TMP Total (both ops):      %5d bytes\n", len(tmpCtxReq)+len(tmpCtxResp)+len(tmpIdReq)+len(tmpIdResp))
	fmt.Println()

	ortbTotal := len(ortbReq) + len(ortbResp)
	tmpTotal := len(tmpCtxReq) + len(tmpCtxResp) + len(tmpIdReq) + len(tmpIdResp)
	fmt.Printf("  Ratio (OpenRTB / TMP):     %.1fx larger\n", float64(ortbTotal)/float64(tmpTotal))
	fmt.Printf("  TMP is %.0f%% smaller than OpenRTB\n", (1-float64(tmpTotal)/float64(ortbTotal))*100)
	fmt.Println()
}

func BenchmarkOpenRTB_Request_Marshal(b *testing.B) {
	req := RealisticOpenRTBRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkOpenRTB_Request_Unmarshal(b *testing.B) {
	data, _ := json.Marshal(RealisticOpenRTBRequest())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req BidRequest
		json.Unmarshal(data, &req)
	}
}

func BenchmarkOpenRTB_RoundTrip(b *testing.B) {
	req := RealisticOpenRTBRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		var got BidRequest
		json.Unmarshal(data, &got)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkOpenRTB_FullExchange(b *testing.B) {
	req := RealisticOpenRTBRequest()
	respFixture := RealisticOpenRTBResponse()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqData, _ := json.Marshal(req)
		var gotReq BidRequest
		json.Unmarshal(reqData, &gotReq)
		respData, _ := json.Marshal(respFixture)
		var gotResp BidResponse
		json.Unmarshal(respData, &gotResp)
		b.SetBytes(int64(len(reqData) + len(respData)))
	}
}

func BenchmarkTMP_FullExchange(b *testing.B) {
	ctxReq := RealisticTMPContextRequest()
	ctxResp := RealisticTMPContextResponse()
	idReq := RealisticTMPIdentityRequest()
	idResp := RealisticTMPIdentityResponse()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctxReqData, _ := json.Marshal(ctxReq)
		var gotCtxReq TMPContextRequest
		json.Unmarshal(ctxReqData, &gotCtxReq)
		ctxRespData, _ := json.Marshal(ctxResp)
		var gotCtxResp TMPContextResponse
		json.Unmarshal(ctxRespData, &gotCtxResp)

		idReqData, _ := json.Marshal(idReq)
		var gotIdReq TMPIdentityRequest
		json.Unmarshal(idReqData, &gotIdReq)
		idRespData, _ := json.Marshal(idResp)
		var gotIdResp TMPIdentityResponse
		json.Unmarshal(idRespData, &gotIdResp)

		b.SetBytes(int64(len(ctxReqData) + len(ctxRespData) + len(idReqData) + len(idRespData)))
	}
}
