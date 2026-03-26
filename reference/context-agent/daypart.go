package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmp"
)

// DaypartFilterModule filters by time-of-day and day-of-week.
// Dayparts stored as Valkey set entries: "mon:9-17", "sat:0-24".
type DaypartFilterModule struct {
	valkey ValkeyClient
	now    func() time.Time // injectable for testing
}

func NewDaypartFilterModule(valkey ValkeyClient) *DaypartFilterModule {
	return &DaypartFilterModule{valkey: valkey, now: time.Now}
}

func (m *DaypartFilterModule) Name() string { return "daypart_filter" }

func (m *DaypartFilterModule) Evaluate(ctx context.Context, _ *tmp.ContextMatchRequest, packages []tmp.AvailablePackage) []ModuleResult {
	results := make([]ModuleResult, 0, len(packages))
	now := m.now()
	dow := strings.ToLower(now.Weekday().String()[:3])
	hour := now.Hour()

	for _, pkg := range packages {
		activate := true

		dayparts, _ := m.valkey.SMembers(ctx, "daypart:"+pkg.PackageID)
		if len(dayparts) > 0 {
			activate = false
			for _, dp := range dayparts {
				if matchDaypart(dp, dow, hour) {
					activate = true
					break
				}
			}
		}

		score := float32(0)
		if activate {
			score = 1.0
		}
		results = append(results, ModuleResult{
			PackageID: pkg.PackageID,
			Activate:  activate,
			Score:     score,
		})
	}
	return results
}

// matchDaypart parses "mon:9-17" and checks if dow/hour falls within.
func matchDaypart(dp, dow string, hour int) bool {
	parts := strings.SplitN(dp, ":", 2)
	if len(parts) != 2 {
		return false
	}
	if parts[0] != dow {
		return false
	}
	hourRange := strings.SplitN(parts[1], "-", 2)
	if len(hourRange) != 2 {
		return false
	}
	start, err1 := strconv.Atoi(hourRange[0])
	end, err2 := strconv.Atoi(hourRange[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return hour >= start && hour < end
}
