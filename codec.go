package cache

import "encoding/json"

// JSONCodec is the dependency-free default codec.
type JSONCodec struct{}

func (JSONCodec) Name() string                         { return "json/v1" }
func (JSONCodec) Marshal(v any) ([]byte, error)        { return json.Marshal(v) }
func (JSONCodec) Unmarshal(data []byte, dst any) error { return json.Unmarshal(data, dst) }
