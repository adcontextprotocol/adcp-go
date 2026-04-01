package tmproto

import "encoding/json"

// MarshalJSON marshals any TMP message type to JSON.
func MarshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UnmarshalJSON unmarshals JSON into any TMP message type.
func UnmarshalJSON(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
