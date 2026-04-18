package tmproto

// IdentityToken carries one opaque user identifier and the type of identity
// graph it came from. Publishers include one entry per token they have; the
// buyer resolves on whichever graph matches. Used by IdentityMatchRequest.
type IdentityToken struct {
	UserToken string  `json:"user_token"`
	UIDType   UIDType `json:"uid_type"`
}
