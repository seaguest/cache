package cache

import (
	"bytes"
	"fmt"
	"log"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/mna/redisc"
)

var redisFactor = 16 // set default redis factor to 16

var cache *Cache

func Init(addrs []string, opts ...redis.DialOption) {
	var getConn func() redis.Conn
	if len(addrs) == 1 {
		pool, _ := getRedisPool(addrs[0], opts...)

		getConn = func() redis.Conn {
			return pool.Get()
		}
	} else {
		cluster := redisc.Cluster{
			StartupNodes: addrs,
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
	}
	cache = New(getConn)
}

func GetKey(args ...interface{}) string {
	if len(args) == 0 {
		return ""
	}

	var buf bytes.Buffer
	buf.WriteString(fmt.Sprint(args[0]))
	for _, k := range args[1:] {
		buf.WriteString("_")
		buf.WriteString(fmt.Sprint(k))
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

func SetRedisFactor(factor int) {
	redisFactor = factor
}
