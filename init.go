package cache

import (
	"bytes"
	"fmt"
)

var cache *Cache

func Init(redisAddr, redisPwd string, maxConn int) {
	cache = New(redisAddr, redisPwd, maxConn)
}

func GetKey(args ...interface{}) string {
	var buf bytes.Buffer
	for i, k := range args {
		buf.WriteString(fmt.Sprint(k))
		if i < len(args)-1 {
			buf.WriteString("_")
		}
	}
	return buf.String()
}

func GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	return cache.GetObject(key, obj, ttl, f)
}

func Disable() {
	cache.Disable()
}

func Delete(key string) error {
	return cache.Delete(key)
}
