package tiercache

import (
	"reflect"
	"time"
)

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func ttlWithJitter(key string, ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	jitterWindow := ttl / 10
	if jitterWindow <= 0 {
		return ttl
	}
	return ttl + time.Duration(fingerprint(key)%uint64(jitterWindow))
}

func (c *Client) versionTTL() time.Duration {
	return c.limits.MaxTTL + c.limits.MaxStale + c.limits.VersionCacheTTL + time.Minute
}
