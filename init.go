package cache

import (
	"bytes"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/mna/redisc"
)

var cache *Cache

func Init(addr interface{}, opts ...redis.DialOption) {
	var getConn func() redis.Conn
	if reflect.TypeOf(addr).Kind() == reflect.String {
		pool, _ := getRedisPool(addr.(string), opts...)

		getConn = func() redis.Conn {
			return pool.Get()
		}
	} else if reflect.TypeOf(addr).Kind() == reflect.Slice && reflect.TypeOf(addr).Elem().Kind() == reflect.String {
		cluster := redisc.Cluster{
			StartupNodes: addr.([]string),
			DialOptions:  []redis.DialOption{redis.DialConnectTimeout(5 * time.Second)},
			CreatePool:   getRedisPool,
		}

		// initialize its mapping
		if err := cluster.Refresh(); err != nil {
			log.Fatalf("Refresh failed: %v", err)
		}

		getConn = func() redis.Conn {
			return cluster.Get()
		}
	} else {
		log.Fatalf("invalid addr [%s]", addr)
	}

	cache = New(getConn)
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
