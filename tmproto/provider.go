package tmproto

import "context"

// ContextProvider evaluates packages against content context.
type ContextProvider interface {
	ContextMatch(ctx context.Context, req *ContextMatchRequest) (*ContextMatchResponse, error)
}

// IdentityProvider evaluates user eligibility for packages.
type IdentityProvider interface {
	IdentityMatch(ctx context.Context, req *IdentityMatchRequest) (*IdentityMatchResponse, error)
}
