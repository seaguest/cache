package tiercache

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type cacheKeyer interface {
	CacheKey() string
}

// MakeKey returns a deterministic, delimiter-safe logical cache key.
func MakeKey(name string, args ...any) Key {
	return makeName[Key](name, args...)
}

// MakeTag returns a deterministic dependency tag for group invalidation.
func MakeTag(name string, args ...any) Tag {
	return makeName[Tag](name, args...)
}

// MakeScope returns a deterministic dependency scope for group invalidation.
func MakeScope(name string, args ...any) Scope {
	return Scope(MakeTag(name, args...))
}

func makeName[T ~string](name string, args ...any) T {
	name = strings.TrimSpace(name)
	if len(args) == 0 {
		return T(name)
	}
	material := canonicalParts(append([]any{name}, args...)...)
	sum := sha256.Sum256(material)
	return T(name + "#" + hex.EncodeToString(sum[:16]))
}

func validateKey(k Key) error {
	if strings.TrimSpace(string(k)) == "" {
		return ErrInvalidKey
	}
	return nil
}

func validateTags(tags []Tag) error {
	for _, tag := range tags {
		if strings.TrimSpace(string(tag)) == "" {
			return ErrInvalidTag
		}
	}
	return nil
}

func canonicalTags(tags []Tag) []Tag {
	if len(tags) == 0 {
		return nil
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

func canonicalParts(parts ...any) []byte {
	var buf bytes.Buffer
	for _, part := range parts {
		encoded := canonicalPart(part)
		buf.WriteString(strconv.Itoa(len(encoded)))
		buf.WriteByte(':')
		buf.Write(encoded)
		buf.WriteByte('|')
	}
	return buf.Bytes()
}

func canonicalPart(v any) []byte {
	if v == nil {
		return []byte("nil")
	}
	if ck, ok := v.(cacheKeyer); ok {
		return []byte("cachekey:" + ck.CacheKey())
	}

	switch x := v.(type) {
	case string:
		return []byte("string:" + x)
	case []byte:
		return []byte("bytes:" + base64.RawStdEncoding.EncodeToString(x))
	case fmt.Stringer:
		return []byte("stringer:" + x.String())
	case int:
		return []byte("int:" + strconv.FormatInt(int64(x), 10))
	case int8:
		return []byte("int8:" + strconv.FormatInt(int64(x), 10))
	case int16:
		return []byte("int16:" + strconv.FormatInt(int64(x), 10))
	case int32:
		return []byte("int32:" + strconv.FormatInt(int64(x), 10))
	case int64:
		return []byte("int64:" + strconv.FormatInt(x, 10))
	case uint:
		return []byte("uint:" + strconv.FormatUint(uint64(x), 10))
	case uint8:
		return []byte("uint8:" + strconv.FormatUint(uint64(x), 10))
	case uint16:
		return []byte("uint16:" + strconv.FormatUint(uint64(x), 10))
	case uint32:
		return []byte("uint32:" + strconv.FormatUint(uint64(x), 10))
	case uint64:
		return []byte("uint64:" + strconv.FormatUint(x, 10))
	case bool:
		return []byte("bool:" + strconv.FormatBool(x))
	case float32:
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], math.Float32bits(x))
		return append([]byte("float32:"), b[:]...)
	case float64:
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(x))
		return append([]byte("float64:"), b[:]...)
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return []byte("nilptr:" + rv.Type().String())
	}

	b, err := json.Marshal(v)
	if err == nil {
		return append([]byte("json:"+reflect.TypeOf(v).String()+":"), b...)
	}
	return []byte("fmt:" + fmt.Sprintf("%#v", v))
}

func hashHex(v []byte) string {
	sum := sha256.Sum256(v)
	return hex.EncodeToString(sum[:])
}

func fingerprint(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

func keyName(k Key) string {
	s := string(k)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
}
