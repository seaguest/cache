package cache

import (
	"reflect"
	"time"
)

func isNil(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// jitteredTTL deterministically subtracts up to 10 percent. Entries never live
// longer than the TTL selected by the caller, and logical expirations are
// distributed rather than only Redis deletion times.
func jitteredTTL(key string, ttl time.Duration) time.Duration {
	if ttl <= 10*time.Millisecond {
		return ttl
	}
	window := ttl / 10
	return ttl - time.Duration(localHash(key)%uint64(window))
}
