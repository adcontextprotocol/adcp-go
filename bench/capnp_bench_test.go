package bench

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"

	capnp "capnproto.org/go/capnp/v3"
)

// --- Cap'n Proto helpers ---
// Uses the raw Cap'n Proto API to build messages without codegen.

func buildCapnpContextRequest(arena *capnp.Arena) []byte {
	msg, seg, _ := capnp.NewMessage(*arena)
	root, _ := capnp.NewRootStruct(seg, capnp.ObjectSize{DataSize: 8, PointerCount: 7})

	root.SetNewText(0, "1.0")
	root.SetNewText(1, "ctx-8f2a-oakwood-91b3")
	root.SetNewText(2, "oakwood-publishing-main")
	root.SetUint16(0, 0) // website = 0
	root.SetNewText(3, "article-sidebar-300x250")

	artifacts, _ := capnp.NewTextList(seg, 1)
	artifacts.Set(0, "article:sustainable-kitchen-2026-03")
	root.SetPtr(4, artifacts.ToPtr())

	pkgList, _ := capnp.NewCompositeList(seg, capnp.ObjectSize{DataSize: 0, PointerCount: 4}, 3)
	for i, pkg := range []struct{ id, mbid string }{
		{"pkg-display-0041", "mb-acme-q1"},
		{"pkg-native-0078", "mb-nova-q1"},
		{"pkg-display-0103", "mb-summit-q1"},
	} {
		s := pkgList.Struct(i)
		s.SetNewText(0, pkg.id)
		s.SetNewText(1, pkg.mbid)
		fmts, _ := capnp.NewTextList(seg, 1)
		fmts.Set(0, "display_300x250")
		s.SetPtr(2, fmts.ToPtr())
	}
	root.SetPtr(5, pkgList.ToPtr())

	data, _ := msg.Marshal()
	return data
}

func buildCapnpIdentityRequest(arena *capnp.Arena) []byte {
	msg, seg, _ := capnp.NewMessage(*arena)
	root, _ := capnp.NewRootStruct(seg, capnp.ObjectSize{DataSize: 8, PointerCount: 5})

	root.SetNewText(0, "1.0")
	root.SetNewText(1, "id-3k9p-oakwood-d4f1")
	root.SetNewText(2, "tok_uid2_AgAAAAVacu1uaxgAAAQ14AAAAABAAAAA")
	root.SetUint16(0, 0) // uid2 = 0

	pkgIds, _ := capnp.NewTextList(seg, 10)
	ids := []string{
		"pkg-display-0041", "pkg-display-0042", "pkg-display-0043",
		"pkg-native-0078", "pkg-native-0079",
		"pkg-display-0103", "pkg-display-0104",
		"pkg-video-0201", "pkg-video-0202",
		"pkg-native-0301",
	}
	for i, id := range ids {
		pkgIds.Set(i, id)
	}
	root.SetPtr(3, pkgIds.ToPtr())

	data, _ := msg.Marshal()
	return data
}

func buildCapnpIdentityResponse(arena *capnp.Arena) []byte {
	msg, seg, _ := capnp.NewMessage(*arena)
	root, _ := capnp.NewRootStruct(seg, capnp.ObjectSize{DataSize: 0, PointerCount: 2})

	root.SetNewText(0, "id-3k9p-oakwood-d4f1")

	eligList, _ := capnp.NewCompositeList(seg, capnp.ObjectSize{DataSize: 8, PointerCount: 1}, 10)
	scores := []float32{0.82, 0, 0, 0.65, 0, 0, 0.41, 0, 0, 0.73}
	eligible := []bool{true, true, false, true, true, false, true, false, true, true}
	pkgIds := []string{
		"pkg-display-0041", "pkg-display-0042", "pkg-display-0043",
		"pkg-native-0078", "pkg-native-0079",
		"pkg-display-0103", "pkg-display-0104",
		"pkg-video-0201", "pkg-video-0202",
		"pkg-native-0301",
	}
	for i := range pkgIds {
		s := eligList.Struct(i)
		s.SetNewText(0, pkgIds[i])
		s.SetBit(0, eligible[i])
		s.SetUint32(4, math.Float32bits(scores[i]))
	}
	root.SetPtr(1, eligList.ToPtr())

	data, _ := msg.Marshal()
	return data
}

func readCapnpContextRequest(data []byte) {
	msg, _ := capnp.Unmarshal(data)
	root, _ := msg.Root()
	s := root.Struct()
	s.Ptr(1) // requestId
	s.Ptr(2) // propertyId
	s.Uint16(0) // propertyType
	s.Ptr(3)    // placementId
}

func readCapnpIdentityRequest(data []byte) {
	msg, _ := capnp.Unmarshal(data)
	root, _ := msg.Root()
	s := root.Struct()
	s.Ptr(1)    // requestId
	s.Ptr(2)    // userToken
	s.Uint16(0) // uidType
}

func readCapnpIdentityResponse(data []byte) {
	msg, _ := capnp.Unmarshal(data)
	root, _ := msg.Root()
	s := root.Struct()
	s.Ptr(0) // requestId
	ptr, _ := s.Ptr(1)
	list := ptr.List()
	for i := 0; i < list.Len(); i++ {
		el := list.Struct(i)
		el.Ptr(0)                                    // packageId
		el.Bit(0)                                    // eligible
		math.Float32frombits(el.Uint32(4))           // intentScore
	}
}

// --- Cap'n Proto Payload Sizes ---

func TestCapnpPayloadSizes(t *testing.T) {
	arena := capnp.SingleSegment(nil)
	ctxReqData := buildCapnpContextRequest(&arena)
	arena = capnp.SingleSegment(nil)
	idReqData := buildCapnpIdentityRequest(&arena)
	arena = capnp.SingleSegment(nil)
	idRespData := buildCapnpIdentityResponse(&arena)

	ortbReq, _ := json.Marshal(RealisticOpenRTBRequest())
	ortbResp, _ := json.Marshal(RealisticOpenRTBResponse())
	tmpCtxReqJSON, _ := json.Marshal(RealisticTMPContextRequest())
	tmpIdReqJSON, _ := json.Marshal(RealisticTMPIdentityRequest())
	tmpIdRespJSON, _ := json.Marshal(RealisticTMPIdentityResponse())

	fmt.Println("\n=== Payload Size: All Three Formats ===")
	fmt.Println()
	fmt.Printf("  %-35s %8s %10s %10s\n", "Message", "OpenRTB", "TMP JSON", "TMP capnp")
	fmt.Printf("  %-35s %8s %10s %10s\n", "-------", "-------", "--------", "---------")
	fmt.Printf("  %-35s %6d B %8d B %8d B\n", "Context Match Request", len(ortbReq), len(tmpCtxReqJSON), len(ctxReqData))
	fmt.Printf("  %-35s %6d B %8d B %8s\n", "Response (context/bid)", len(ortbResp), 434, "~similar")
	fmt.Printf("  %-35s %8s %8d B %8d B\n", "Identity Match Request", "n/a", len(tmpIdReqJSON), len(idReqData))
	fmt.Printf("  %-35s %8s %8d B %8d B\n", "Identity Match Response", "n/a", len(tmpIdRespJSON), len(idRespData))
	fmt.Println()

	ortbTotal := len(ortbReq) + len(ortbResp)
	tmpJSONTotal := len(tmpCtxReqJSON) + 434 + len(tmpIdReqJSON) + len(tmpIdRespJSON)
	tmpCapnpTotal := len(ctxReqData) + len(idReqData) + len(idRespData)

	fmt.Printf("  OpenRTB JSON total:  %d bytes\n", ortbTotal)
	fmt.Printf("  TMP JSON total:      %d bytes (%.0f%% of OpenRTB)\n", tmpJSONTotal, float64(tmpJSONTotal)/float64(ortbTotal)*100)
	fmt.Printf("  TMP Cap'n Proto:     %d bytes (%.0f%% of OpenRTB)\n", tmpCapnpTotal, float64(tmpCapnpTotal)/float64(ortbTotal)*100)
	fmt.Println()
}

// --- Cap'n Proto Serialization Benchmarks ---

func BenchmarkCapnp_ContextRequest_Marshal(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		arena := capnp.SingleSegment(nil)
		data := buildCapnpContextRequest(&arena)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkCapnp_ContextRequest_Unmarshal(b *testing.B) {
	arena := capnp.SingleSegment(nil)
	data := buildCapnpContextRequest(&arena)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readCapnpContextRequest(data)
	}
}

func BenchmarkCapnp_IdentityRequest_Marshal(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		arena := capnp.SingleSegment(nil)
		data := buildCapnpIdentityRequest(&arena)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkCapnp_IdentityRequest_Unmarshal(b *testing.B) {
	arena := capnp.SingleSegment(nil)
	data := buildCapnpIdentityRequest(&arena)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readCapnpIdentityRequest(data)
	}
}

func BenchmarkCapnp_IdentityResponse_Marshal(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		arena := capnp.SingleSegment(nil)
		data := buildCapnpIdentityResponse(&arena)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkCapnp_IdentityResponse_Unmarshal(b *testing.B) {
	arena := capnp.SingleSegment(nil)
	data := buildCapnpIdentityResponse(&arena)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		readCapnpIdentityResponse(data)
	}
}

// --- Parallel TMP Exchange (JSON) ---

func BenchmarkTMP_FullExchange_Parallel_JSON(b *testing.B) {
	ctxReq := RealisticTMPContextRequest()
	ctxResp := RealisticTMPContextResponse()
	idReq := RealisticTMPIdentityRequest()
	idResp := RealisticTMPIdentityResponse()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		var ctxReqData, ctxRespData, idReqData, idRespData []byte

		wg.Add(2)
		go func() {
			defer wg.Done()
			ctxReqData, _ = json.Marshal(ctxReq)
			var got TMPContextRequest
			json.Unmarshal(ctxReqData, &got)
			ctxRespData, _ = json.Marshal(ctxResp)
			var gotR TMPContextResponse
			json.Unmarshal(ctxRespData, &gotR)
		}()
		go func() {
			defer wg.Done()
			idReqData, _ = json.Marshal(idReq)
			var got TMPIdentityRequest
			json.Unmarshal(idReqData, &got)
			idRespData, _ = json.Marshal(idResp)
			var gotR TMPIdentityResponse
			json.Unmarshal(idRespData, &gotR)
		}()
		wg.Wait()
		b.SetBytes(int64(len(ctxReqData) + len(ctxRespData) + len(idReqData) + len(idRespData)))
	}
}

// --- Parallel TMP Exchange (Cap'n Proto) ---

func BenchmarkTMP_FullExchange_Parallel_Capnp(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var wg sync.WaitGroup
		var ctxData, idReqData, idRespData []byte

		wg.Add(2)
		go func() {
			defer wg.Done()
			arena := capnp.SingleSegment(nil)
			ctxData = buildCapnpContextRequest(&arena)
			readCapnpContextRequest(ctxData)
		}()
		go func() {
			defer wg.Done()
			arena := capnp.SingleSegment(nil)
			idReqData = buildCapnpIdentityRequest(&arena)
			readCapnpIdentityRequest(idReqData)
			arena2 := capnp.SingleSegment(nil)
			idRespData = buildCapnpIdentityResponse(&arena2)
			readCapnpIdentityResponse(idRespData)
		}()
		wg.Wait()
		b.SetBytes(int64(len(ctxData) + len(idReqData) + len(idRespData)))
	}
}
