package tmp

import "context"

// ContextProvider evaluates packages against content context.
type ContextProvider interface {
	ContextMatch(ctx context.Context, req *ContextMatchRequest) (*ContextMatchResponse, error)
}

// IdentityProvider evaluates user eligibility for packages.
type IdentityProvider interface {
	IdentityMatch(ctx context.Context, req *IdentityMatchRequest) (*IdentityMatchResponse, error)
}

// Codec handles serialization and deserialization of TMP messages.
type Codec interface {
	ContentType() string
	MarshalContextRequest(req *ContextMatchRequest) ([]byte, error)
	UnmarshalContextRequest(data []byte) (*ContextMatchRequest, error)
	MarshalContextResponse(resp *ContextMatchResponse) ([]byte, error)
	UnmarshalContextResponse(data []byte) (*ContextMatchResponse, error)
	MarshalIdentityRequest(req *IdentityMatchRequest) ([]byte, error)
	UnmarshalIdentityRequest(data []byte) (*IdentityMatchRequest, error)
	MarshalIdentityResponse(resp *IdentityMatchResponse) ([]byte, error)
	UnmarshalIdentityResponse(data []byte) (*IdentityMatchResponse, error)
}
