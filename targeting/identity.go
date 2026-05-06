package targeting

import (
	"github.com/adcontextprotocol/adcp-go/tmproto"
)

// UserIdentity represents a user identity token with its type.
type UserIdentity struct {
	UserToken string `json:"user_token"`
	UIDType   string `json:"uid_type"`
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
