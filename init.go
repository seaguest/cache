package cache

import (
	"bytes"
	"strconv"
)

var cache *Cache

func Init(redisAddr, redisPwd string, maxconn int) {
	cache = New(redisAddr, redisPwd, maxconn)
}

func GetKey(args ...interface{}) string {
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
	}

	var buf bytes.Buffer
	for i, k := range args {
		addBuf(k, &buf)
		if i < len(args)-1 {
			addBuf("_", &buf)
		}
	}
	return buf.String()
}

func GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	return cache.GetObject(key, obj, ttl, f)
}

func EnableDebug() {
	cache.EnableDebug()
}

func Delete(key string) error {
	return cache.Delete(key)
}
