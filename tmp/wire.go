package tmp

import "encoding/json"

// JSONCodec implements Codec using standard JSON encoding.
type JSONCodec struct{}

func (c *JSONCodec) ContentType() string { return "application/json" }

func (c *JSONCodec) MarshalContextRequest(req *ContextMatchRequest) ([]byte, error) {
	return json.Marshal(req)
}

func (c *JSONCodec) UnmarshalContextRequest(data []byte) (*ContextMatchRequest, error) {
	var req ContextMatchRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *JSONCodec) MarshalContextResponse(resp *ContextMatchResponse) ([]byte, error) {
	return json.Marshal(resp)
}

func (c *JSONCodec) UnmarshalContextResponse(data []byte) (*ContextMatchResponse, error) {
	var resp ContextMatchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *JSONCodec) MarshalIdentityRequest(req *IdentityMatchRequest) ([]byte, error) {
	return json.Marshal(req)
}

func (c *JSONCodec) UnmarshalIdentityRequest(data []byte) (*IdentityMatchRequest, error) {
	var req IdentityMatchRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func (c *JSONCodec) MarshalIdentityResponse(resp *IdentityMatchResponse) ([]byte, error) {
	return json.Marshal(resp)
}

func (c *JSONCodec) UnmarshalIdentityResponse(data []byte) (*IdentityMatchResponse, error) {
	var resp IdentityMatchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
