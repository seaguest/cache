package cache

import (
	"bytes"
	"strconv"
)

var cache *Cache

func Init(redisAddr, redisPassword string, lazy bool, maxconn int) {
	cache = New(redisAddr, redisPassword, lazy, maxconn)
}

func GetCacheKey(args ...interface{}) string {
	addBuf := func(i interface{}, bf *bytes.Buffer) {
		switch v := i.(type) {
		case int:
			bf.WriteString(strconv.Itoa(v))
		case int8:
			bf.WriteString(strconv.Itoa(int(v)))
		case int16:
			bf.WriteString(strconv.Itoa(int(v)))
		case int32:
			bf.WriteString(strconv.Itoa(int(v)))
		case int64:
			bf.WriteString(strconv.Itoa(int(v)))
		case uint8:
			bf.WriteString(strconv.Itoa(int(v)))
		case uint16:
			bf.WriteString(strconv.Itoa(int(v)))
		case uint32:
			bf.WriteString(strconv.Itoa(int(v)))
		case uint64:
			bf.WriteString(strconv.Itoa(int(v)))
		case string:
			bf.WriteString(v)
		}
		bf.WriteString("_")
	}

	var buf bytes.Buffer
	for _, k := range args {
		addBuf(k, &buf)
	}
	return buf.String()
}

func GetCacheObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	return cache.GetObject(key, obj, ttl, f)
}

func Delete(key string) {
	cache.Delete(key)
}
