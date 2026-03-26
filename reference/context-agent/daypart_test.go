package main

import (
	"context"
	"testing"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

func TestDaypartFilter_InWindow(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("daypart:pkg-1", "mon:9-17", "tue:9-17")

	mod := NewDaypartFilterModule(v)
	// Monday at 10am
	mod.now = func() time.Time { return time.Date(2026, 3, 23, 10, 0, 0, 0, time.UTC) }

	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("Monday 10am should match mon:9-17")
	}
}

func TestDaypartFilter_OutsideWindow(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("daypart:pkg-1", "mon:9-17")

	mod := NewDaypartFilterModule(v)
	// Monday at 8am (before window)
	mod.now = func() time.Time { return time.Date(2026, 3, 23, 8, 0, 0, 0, time.UTC) }

	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("Monday 8am should not match mon:9-17")
	}
}

func TestDaypartFilter_WrongDay(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("daypart:pkg-1", "mon:9-17")

	mod := NewDaypartFilterModule(v)
	// Tuesday at 10am
	mod.now = func() time.Time { return time.Date(2026, 3, 24, 10, 0, 0, 0, time.UTC) }

	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if results[0].Activate {
		t.Error("Tuesday should not match mon:9-17")
	}
}

func TestDaypartFilter_NoDayparts(t *testing.T) {
	v := NewMockValkeyClient()
	mod := NewDaypartFilterModule(v)

	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("no dayparts means always active")
	}
}

func TestDaypartFilter_AllDay(t *testing.T) {
	v := NewMockValkeyClient()
	v.SAdd("daypart:pkg-1", "sat:0-24")

	mod := NewDaypartFilterModule(v)
	// Saturday at 23:00
	mod.now = func() time.Time { return time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC) }

	results := mod.Evaluate(context.Background(), &tmp.ContextMatchRequest{}, []tmp.AvailablePackage{{PackageID: "pkg-1"}})

	if !results[0].Activate {
		t.Error("sat:0-24 should match any hour on Saturday")
	}
}

func TestMatchDaypart(t *testing.T) {
	tests := []struct {
		dp    string
		dow   string
		hour  int
		match bool
	}{
		{"mon:9-17", "mon", 9, true},
		{"mon:9-17", "mon", 16, true},
		{"mon:9-17", "mon", 17, false},
		{"mon:9-17", "tue", 10, false},
		{"sat:0-24", "sat", 0, true},
		{"sat:0-24", "sat", 23, true},
		{"invalid", "mon", 10, false},
		{"mon:bad-17", "mon", 10, false},
	}
	for _, tt := range tests {
		if got := matchDaypart(tt.dp, tt.dow, tt.hour); got != tt.match {
			t.Errorf("matchDaypart(%q, %q, %d) = %v, want %v", tt.dp, tt.dow, tt.hour, got, tt.match)
		}
	}
}
