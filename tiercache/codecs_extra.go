package tiercache

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
	"google.golang.org/protobuf/proto"
)

type GobCodec struct{}

func (GobCodec) Name() string { return "gob" }

func (GobCodec) Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (GobCodec) Unmarshal(data []byte, dst any) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(dst)
}

type MsgpackCodec struct{}

func (MsgpackCodec) Name() string { return "msgpack" }

func (MsgpackCodec) Marshal(v any) ([]byte, error) {
	return msgpack.Marshal(v)
}

func (MsgpackCodec) Unmarshal(data []byte, dst any) error {
	return msgpack.Unmarshal(data, dst)
}

type ProtoCodec struct{}

func (ProtoCodec) Name() string { return "proto" }

func (ProtoCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("tiercache: %T does not implement proto.Message", v)
	}
	return proto.Marshal(msg)
}

func (ProtoCodec) Unmarshal(data []byte, dst any) error {
	if msg, ok := dst.(proto.Message); ok {
		return proto.Unmarshal(data, msg)
	}

	rv := reflect.ValueOf(dst)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Pointer {
		return fmt.Errorf("tiercache: %T does not implement proto.Message", dst)
	}
	if rv.Elem().IsNil() {
		rv.Elem().Set(reflect.New(rv.Elem().Type().Elem()))
	}
	msg, ok := rv.Elem().Interface().(proto.Message)
	if !ok {
		return fmt.Errorf("tiercache: %T does not implement proto.Message", dst)
	}
	return proto.Unmarshal(data, msg)
}
