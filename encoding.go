package cache

import (
	"encoding/json"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func marshal(v interface{}) ([]byte, error) {
	protoVal, ok := v.(proto.Message)
	if ok {
		return protojson.Marshal(protoVal)
	} else {
		return json.Marshal(v)
	}
}

func unmarshal(data []byte, v interface{}) error {
	protoVal, ok := v.(proto.Message)
	if ok {
		return protojson.Unmarshal(data, protoVal)
	} else {
		return json.Unmarshal(data, v)
	}
}
