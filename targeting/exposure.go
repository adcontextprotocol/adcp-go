package targeting

import (
	"encoding/json"

	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// UserIdentity represents a user identity token with its type.
type UserIdentity struct {
	UserToken string `json:"user_token"`
	UIDType   string `json:"uid_type"`
}

// UserProfile holds a user's segment memberships with optional intent scores.
type UserProfile struct {
	Segments map[string]float64 `json:"segments"` // segment name → score (0 = member, no score)
}

// ParseUserProfile parses a JSON-serialized user profile.
func ParseUserProfile(data string) *UserProfile {
	if data == "" {
		return nil
	}
	var p UserProfile
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil
	}
	return &p
}

// MergeUserProfiles unions segment memberships across multiple profiles.
// For duplicate segments, takes the higher score.
func MergeUserProfiles(profiles ...*UserProfile) *UserProfile {
	merged := &UserProfile{Segments: make(map[string]float64)}
	for _, p := range profiles {
		if p == nil {
			continue
		}
		for seg, score := range p.Segments {
			if existing, ok := merged.Segments[seg]; !ok || score > existing {
				merged.Segments[seg] = score
			}
		}
	}
	return merged
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
