package tmproto

import "context"

// ContextProvider evaluates packages against content context. The response
// uses the provider→router shape (ProviderContextMatchResponse): offers,
// optional cache guidance, and response-level signals in the provider's own
// targeting vocabulary. Router-hop fields (`signals_by_provider`) MUST NOT
// be populated; the router assembles those on the merged router→publisher
// response.
type ContextProvider interface {
	ContextMatch(ctx context.Context, req *ContextMatchRequest) (*ProviderContextMatchResponse, error)
}

// IdentityProvider evaluates user eligibility for packages. The response
// uses the provider→router shape (ProviderIdentityMatchResponse): eligibility,
// serve-window throttle, and — when the provider mints one — ordered TMPX
// chunks. Router-hop fields (`tmpx_providers`, `tmpx`) MUST NOT be populated;
// the router assembles those on the merged router→publisher response.
type IdentityProvider interface {
	IdentityMatch(ctx context.Context, req *IdentityMatchRequest) (*ProviderIdentityMatchResponse, error)
}
