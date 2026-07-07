package facts

import (
	"encoding/json"
)

// Codec encodes and decodes fact payloads.
type Codec interface {
	Encode(value any) ([]byte, error)
	Decode(data []byte, dest any) error
}

// JSONCodec stores fact payloads as JSON.
type JSONCodec struct{}

// Encode implements Codec.
func (JSONCodec) Encode(value any) ([]byte, error) {
	return json.Marshal(value)
}

// Decode implements Codec.
func (JSONCodec) Decode(data []byte, dest any) error {
	return json.Unmarshal(data, dest)
}
