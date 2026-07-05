package tiercache

import "encoding/json"

// JSONCodec encodes values as JSON.
type JSONCodec struct{}

func (JSONCodec) Name() string { return "json" }

func (JSONCodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (JSONCodec) Unmarshal(data []byte, dst any) error {
	return json.Unmarshal(data, dst)
}
