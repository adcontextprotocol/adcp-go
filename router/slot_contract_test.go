package router

import (
	"testing"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

func TestEnforceProviderSlotContract(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		chunks     []tmproto.TmpxChunk
		want       bool
	}{
		{
			name:       "single-slot exact prefix",
			registered: []string{"primary"},
			chunks:     []tmproto.TmpxChunk{{SlotID: "primary", Value: "v1"}},
			want:       true,
		},
		{
			name:       "two-slot exact prefix",
			registered: []string{"primary", "secondary"},
			chunks: []tmproto.TmpxChunk{
				{SlotID: "primary", Value: "v1"},
				{SlotID: "secondary", Value: "v2"},
			},
			want: true,
		},
		{
			name:       "one-of-two ordered prefix",
			registered: []string{"primary", "secondary"},
			chunks:     []tmproto.TmpxChunk{{SlotID: "primary", Value: "v1"}},
			want:       true,
		},
		{
			name:       "empty emitted — no non-empty prefix",
			registered: []string{"primary"},
			chunks:     nil,
			want:       false,
		},
		{
			name:       "longer than registered",
			registered: []string{"primary"},
			chunks: []tmproto.TmpxChunk{
				{SlotID: "primary", Value: "v1"},
				{SlotID: "secondary", Value: "v2"},
			},
			want: false,
		},
		{
			name:       "unregistered slot_id",
			registered: []string{"primary", "secondary"},
			chunks:     []tmproto.TmpxChunk{{SlotID: "tertiary", Value: "v1"}},
			want:       false,
		},
		{
			name:       "reordered vs registered",
			registered: []string{"primary", "secondary"},
			chunks: []tmproto.TmpxChunk{
				{SlotID: "secondary", Value: "v1"},
				{SlotID: "primary", Value: "v2"},
			},
			want: false,
		},
		{
			name:       "sparse — skips registered slot",
			registered: []string{"primary", "secondary"},
			chunks:     []tmproto.TmpxChunk{{SlotID: "secondary", Value: "v1"}},
			want:       false,
		},
		{
			name:       "duplicate slot_id",
			registered: []string{"primary", "secondary"},
			chunks: []tmproto.TmpxChunk{
				{SlotID: "primary", Value: "v1"},
				{SlotID: "primary", Value: "v2"},
			},
			want: false,
		},
		{
			name:       "provider not registered — empty registered list",
			registered: nil,
			chunks:     []tmproto.TmpxChunk{{SlotID: "primary", Value: "v1"}},
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := enforceProviderSlotContract(tt.registered, tt.chunks); got != tt.want {
				t.Fatalf("enforceProviderSlotContract(%v, %v) = %v, want %v", tt.registered, tt.chunks, got, tt.want)
			}
		})
	}
}
