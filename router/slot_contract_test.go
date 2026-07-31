package router

import "testing"

func TestEnforceProviderSlotContract(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		emitted    []string
		want       bool
	}{
		{
			name:       "single-slot exact prefix",
			registered: []string{"primary"},
			emitted:    []string{"primary"},
			want:       true,
		},
		{
			name:       "two-slot exact prefix",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"primary", "secondary"},
			want:       true,
		},
		{
			name:       "one-of-two ordered prefix",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"primary"},
			want:       true,
		},
		{
			name:       "empty emitted — no non-empty prefix",
			registered: []string{"primary"},
			emitted:    nil,
			want:       false,
		},
		{
			name:       "longer than registered",
			registered: []string{"primary"},
			emitted:    []string{"primary", "secondary"},
			want:       false,
		},
		{
			name:       "unregistered slot_id",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"tertiary"},
			want:       false,
		},
		{
			name:       "reordered vs registered",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"secondary", "primary"},
			want:       false,
		},
		{
			name:       "sparse — skips registered slot",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"secondary"},
			want:       false,
		},
		{
			name:       "duplicate slot_id",
			registered: []string{"primary", "secondary"},
			emitted:    []string{"primary", "primary"},
			want:       false,
		},
		{
			name:       "provider not registered — empty registered list",
			registered: nil,
			emitted:    []string{"primary"},
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := enforceProviderSlotContract(tt.registered, tt.emitted); got != tt.want {
				t.Fatalf("enforceProviderSlotContract(%v, %v) = %v, want %v", tt.registered, tt.emitted, got, tt.want)
			}
		})
	}
}
