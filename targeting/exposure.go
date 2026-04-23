package targeting

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// UserIdentity represents a user identity token with its type.
type UserIdentity struct {
	UserToken string `json:"user_token"`
	UIDType   string `json:"uid_type"`
}

// ExposeRequest records an ad impression against a user.
type ExposeRequest struct {
	PackageID    string         `json:"package_id"`
	UserToken    string         `json:"user_token,omitempty"`
	UIDType      string         `json:"uid_type,omitempty"`
	Identities   []UserIdentity `json:"identities,omitempty"`
	ImpressionID string         `json:"impression_id,omitempty"`
	CampaignID   string         `json:"campaign_id,omitempty"`
	SourceID     string         `json:"source_id,omitempty"`
}

// ExposeResponse is returned after recording an exposure.
type ExposeResponse struct {
	PackageID         string `json:"package_id"`
	CampaignCount     int    `json:"campaign_count,omitempty"`
	CampaignRemaining int    `json:"campaign_remaining,omitempty"`
}

// ValidateExposeRequest checks that required fields are present.
func ValidateExposeRequest(req *ExposeRequest) error {
	if req.PackageID == "" {
		return errors.New("package_id is required")
	}
	if req.UserToken == "" && len(req.Identities) == 0 {
		return errors.New("user_token or identities is required")
	}
	return nil
}

// ExposureEntry represents a single ad impression recorded against a user.
type ExposureEntry struct {
	ImpressionID string `json:"id"`
	PackageID    string `json:"pkg"`
	CampaignID   string `json:"cmp,omitempty"`
	SourceID     string `json:"src,omitempty"`
	Timestamp    int64  `json:"ts"`
}

// ExposureLog is a user's exposure history, sorted by timestamp descending.
type ExposureLog []ExposureEntry

// ParseExposureLog parses a JSON-serialized exposure log.
func ParseExposureLog(data string) ExposureLog {
	if data == "" {
		return nil
	}
	var log ExposureLog
	if err := json.Unmarshal([]byte(data), &log); err != nil {
		return nil
	}
	return log
}

// SerializeExposureLog serializes an exposure log to JSON.
func SerializeExposureLog(log ExposureLog) string {
	if len(log) == 0 {
		return "[]"
	}
	data, _ := json.Marshal(log)
	return string(data)
}

// MergeExposureLogs unions multiple exposure logs, deduplicating by ImpressionID.
func MergeExposureLogs(logs ...ExposureLog) ExposureLog {
	seen := make(map[string]struct{})
	var merged ExposureLog
	for _, log := range logs {
		for _, e := range log {
			if _, dup := seen[e.ImpressionID]; !dup {
				seen[e.ImpressionID] = struct{}{}
				merged = append(merged, e)
			}
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Timestamp > merged[j].Timestamp // newest first
	})
	return merged
}

// PruneExpired removes entries older than the cutoff (unix seconds).
func PruneExpired(log ExposureLog, cutoff int64) ExposureLog {
	var pruned ExposureLog
	for _, e := range log {
		if e.Timestamp >= cutoff {
			pruned = append(pruned, e)
		}
	}
	return pruned
}

// CheckFrequencyRules checks if any frequency rule is exceeded for the given
// filter (package or campaign). Returns true if capped.
//
// filterField is "pkg" or "cmp". filterValue is the package or campaign ID.
func CheckFrequencyRules(log ExposureLog, filterField, filterValue string, rules []FrequencyRule, now time.Time) bool {
	for _, rule := range rules {
		cutoff := now.Add(-rule.Window).Unix()
		count := 0
		for _, e := range log {
			if e.Timestamp < cutoff {
				continue
			}
			match := false
			switch filterField {
			case "pkg":
				match = e.PackageID == filterValue
			case "cmp":
				match = e.CampaignID == filterValue
			}
			if match {
				count++
			}
		}
		if count >= rule.MaxCount {
			return true
		}
	}
	return false
}

// LatestExposureTime returns the most recent exposure timestamp for a package.
// Returns 0 if no exposure found.
func LatestExposureTime(log ExposureLog, packageID string) int64 {
	var latest int64
	for _, e := range log {
		if e.PackageID == packageID && e.Timestamp > latest {
			latest = e.Timestamp
		}
	}
	return latest
}

// ComputeIntentScore calculates a recency-based intent score from an exposure timestamp.
// Linear decay from 1.0 to 0.0 over 7 days.
func ComputeIntentScore(exposureTime int64, now time.Time) float64 {
	if exposureTime == 0 {
		return 0
	}
	hoursSince := now.Sub(time.Unix(exposureTime, 0)).Hours()
	score := 1.0 - (hoursSince / 168.0)
	if score < 0 {
		return 0
	}
	return score
}

// resolveIdentities extracts UserIdentity entries from an identity match request.
func resolveIdentities(req *tmproto.IdentityMatchRequest) []UserIdentity {
	if len(req.Identities) == 0 {
		return nil
	}
	out := make([]UserIdentity, len(req.Identities))
	for i, id := range req.Identities {
		out[i] = UserIdentity{UserToken: id.UserToken, UIDType: string(id.UIDType)}
	}
	return out
}

// resolveExposeIdentities extracts UserIdentity entries from an expose request.
func resolveExposeIdentities(req *ExposeRequest) []UserIdentity {
	if len(req.Identities) > 0 {
		return req.Identities
	}
	if req.UserToken != "" {
		return []UserIdentity{{UserToken: req.UserToken, UIDType: req.UIDType}}
	}
	return nil
}
