package cache

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type Item struct {
	Object   interface{} `json:"object"`    // object
	Size     int         `json:"size"`      // object size, in bytes.
	ExpireAt int64       `json:"expire_at"` // data expiration timestamp. in milliseconds.
}

func newItem(v interface{}, ttl time.Duration) *Item {
	var expiredAt int64
	if ttl > 0 {
		expiredAt = time.Now().Add(ttl).UnixMilli()
	}

	return &Item{
		Object:   v,
		ExpireAt: expiredAt,
	}
}

func (it *Item) Expired() bool {
	return it.ExpireAt != 0 && it.ExpireAt < time.Now().UnixMilli()
}

func (i *Item) MarshalJSON() ([]byte, error) {
	type Alias Item
	obj := struct {
		Object json.RawMessage `json:"object"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	var err error
	if pm, ok := i.Object.(proto.Message); ok {
		// Use protojson for proto.Message
		obj.Object, err = protojson.Marshal(pm)
	} else {
		// Use json.Marshal for other types
		obj.Object, err = json.Marshal(i.Object)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

func (i *Item) UnmarshalJSON(data []byte) error {
	type Alias Item
	aux := &struct {
		Object json.RawMessage `json:"object"`
		*Alias
	}{
		Alias: (*Alias)(i),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Replace this with your actual proto.Message
	if pm, ok := i.Object.(proto.Message); ok {
		if err := protojson.Unmarshal(aux.Object, pm); err != nil {
			return err
		}
		i.Object = pm
		return nil
	}
	return json.Unmarshal(aux.Object, i.Object)
}
