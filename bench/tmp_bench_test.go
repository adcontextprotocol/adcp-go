package bench

import (
	"encoding/json"
	"testing"
)

// TMP serialization benchmarks. Run manually (not in CI).
//   go test -bench=^BenchmarkTMP_ -benchmem ./bench/...

func BenchmarkTMP_ContextRequest_Marshal(b *testing.B) {
	req := RealisticTMPContextRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkTMP_ContextRequest_Unmarshal(b *testing.B) {
	data, _ := json.Marshal(RealisticTMPContextRequest())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req TMPContextRequest
		json.Unmarshal(data, &req)
	}
}

func BenchmarkTMP_ContextResponse_Marshal(b *testing.B) {
	resp := RealisticTMPContextResponse()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(resp)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkTMP_IdentityRequest_Marshal(b *testing.B) {
	req := RealisticTMPIdentityRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkTMP_IdentityRequest_Unmarshal(b *testing.B) {
	data, _ := json.Marshal(RealisticTMPIdentityRequest())
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req TMPIdentityRequest
		json.Unmarshal(data, &req)
	}
}

func BenchmarkTMP_IdentityResponse_Marshal(b *testing.B) {
	resp := RealisticTMPIdentityResponse()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(resp)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkTMP_Context_RoundTrip(b *testing.B) {
	req := RealisticTMPContextRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		var got TMPContextRequest
		json.Unmarshal(data, &got)
		b.SetBytes(int64(len(data)))
	}
}

func BenchmarkTMP_Identity_RoundTrip(b *testing.B) {
	req := RealisticTMPIdentityRequest()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, _ := json.Marshal(req)
		var got TMPIdentityRequest
		json.Unmarshal(data, &got)
		b.SetBytes(int64(len(data)))
	}
}
