package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// CacheKeyer gives application types explicit control over key encoding.
type CacheKeyer interface {
	CacheKey() string
}

const (
	maxLogicalKeyLength = 2048
	maxTagLength        = 1024
)

// MakeKey constructs a deterministic logical key. It panics when an argument
// cannot be encoded deterministically; use BuildKey when arguments are not
// known to be valid at compile time.
func MakeKey(name string, args ...any) Key {
	key, err := BuildKey(name, args...)
	if err != nil {
		panic(err)
	}
	return key
}

// BuildKey constructs a deterministic logical key.
func BuildKey(name string, args ...any) (Key, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrInvalidKey
	}
	if len(args) == 0 {
		return Key(name), nil
	}
	material, err := canonicalParts(append([]any{name}, args...)...)
	if err != nil {
		return "", fmt.Errorf("cache: build key %q: %w", name, err)
	}
	sum := sha256.Sum256(material)
	return Key(name + "#" + hex.EncodeToString(sum[:16])), nil
}

// MakeTag constructs a deterministic dependency tag.
func MakeTag(name string, args ...any) Tag {
	key := MakeKey(name, args...)
	return Tag(key)
}

func validateKey(key Key) error {
	if strings.TrimSpace(string(key)) == "" || len(key) > maxLogicalKeyLength {
		return ErrInvalidKey
	}
	return nil
}

func validateTags(tags []Tag) error {
	for _, tag := range tags {
		if strings.TrimSpace(string(tag)) == "" || len(tag) > maxTagLength {
			return ErrInvalidTag
		}
	}
	return nil
}

func canonicalTags(tags []Tag) []Tag {
	if len(tags) == 0 {
		return nil
	}
	if len(tags) == 1 {
		return tags
	}
	out := append([]Tag(nil), tags...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	n := 0
	for _, tag := range out {
		if n == 0 || out[n-1] != tag {
			out[n] = tag
			n++
		}
	}
	return out[:n]
}

func canonicalParts(parts ...any) ([]byte, error) {
	var buf bytes.Buffer
	for _, part := range parts {
		encoded, err := canonicalPart(part)
		if err != nil {
			return nil, err
		}
		buf.WriteString(strconv.Itoa(len(encoded)))
		buf.WriteByte(':')
		buf.Write(encoded)
		buf.WriteByte('|')
	}
	return buf.Bytes(), nil
}

func canonicalPart(v any) ([]byte, error) {
	if v == nil {
		return []byte("nil"), nil
	}
	if keyer, ok := v.(CacheKeyer); ok {
		return []byte("key:" + keyer.CacheKey()), nil
	}
	switch value := v.(type) {
	case string:
		return []byte("string:" + value), nil
	case []byte:
		return []byte("bytes:" + base64.RawStdEncoding.EncodeToString(value)), nil
	case int:
		return []byte("int:" + strconv.FormatInt(int64(value), 10)), nil
	case int8:
		return []byte("int8:" + strconv.FormatInt(int64(value), 10)), nil
	case int16:
		return []byte("int16:" + strconv.FormatInt(int64(value), 10)), nil
	case int32:
		return []byte("int32:" + strconv.FormatInt(int64(value), 10)), nil
	case int64:
		return []byte("int64:" + strconv.FormatInt(value, 10)), nil
	case uint:
		return []byte("uint:" + strconv.FormatUint(uint64(value), 10)), nil
	case uint8:
		return []byte("uint8:" + strconv.FormatUint(uint64(value), 10)), nil
	case uint16:
		return []byte("uint16:" + strconv.FormatUint(uint64(value), 10)), nil
	case uint32:
		return []byte("uint32:" + strconv.FormatUint(uint64(value), 10)), nil
	case uint64:
		return []byte("uint64:" + strconv.FormatUint(value, 10)), nil
	case bool:
		return []byte("bool:" + strconv.FormatBool(value)), nil
	case float32:
		var data [4]byte
		binary.BigEndian.PutUint32(data[:], math.Float32bits(value))
		return append([]byte("float32:"), data[:]...), nil
	case float64:
		var data [8]byte
		binary.BigEndian.PutUint64(data[:], math.Float64bits(value))
		return append([]byte("float64:"), data[:]...), nil
	}

	t := reflect.TypeOf(v)
	value := reflect.ValueOf(v)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return []byte("nilptr:" + stableTypeName(t)), nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("unsupported key argument %s: %w", stableTypeName(t), err)
	}
	return append([]byte("json:"+stableTypeName(t)+":"), data...), nil
}

func stableTypeName(t reflect.Type) string {
	if t == nil {
		return "nil"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + stableTypeName(t.Elem())
	case reflect.Slice:
		return "[]" + stableTypeName(t.Elem())
	case reflect.Array:
		return "[" + strconv.Itoa(t.Len()) + "]" + stableTypeName(t.Elem())
	case reflect.Map:
		return "map[" + stableTypeName(t.Key()) + "]" + stableTypeName(t.Elem())
	}
	if t.Name() != "" {
		if t.PkgPath() != "" {
			return t.PkgPath() + "." + t.Name()
		}
		return t.Name()
	}
	return t.String()
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func keyName(key Key) string {
	name := string(key)
	if i := strings.IndexByte(name, '#'); i >= 0 {
		return name[:i]
	}
	return name
}
