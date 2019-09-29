package cache

import (
	"fmt"
	"reflect"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/json-iterator/go"
	"github.com/mohae/deepcopy"
)

const (
	lazyFactor    = 256
	delKeyChannel = "delkey"
)

type Cache struct {
	// redis connection, handle Pushlish/Subscribe at key deletion
	pool *redis.Pool

	// rds cache, handle redis level cache
	rds *RedisCache

	// mem cache, handle memory cache operation
	mem *MemCache

	// in lazy mode, key will be alive in lazyFactor*TTL even the data is outdated
	lazyMode bool
}

type LoadFunc func() (interface{}, error)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func New(redisAddr, redisPassword string, lazyMode bool, maxConnection int) *Cache {
	c := &Cache{}
	c.pool = GetRedisPool(redisAddr, redisPassword, maxConnection)
	c.mem = NewMemCache(time.Second * 2)
	c.rds = NewRedisCache(c.pool, c.mem)
	c.lazyMode = lazyMode

	// subscribe key deletion
	go c.subscribe(delKeyChannel)
	return c
}

// sync memcache from redis
func (c *Cache) syncMem(key string, copy interface{}, ttl int, f LoadFunc) {
	it, ok := c.rds.Get(key, copy)
	// if key not exists in redis or data outdated, then load from redis
	if !ok || it.Outdated() {
		c.rds.load(key, nil, ttl, c.lazyMode, f, false)
		return
	}
	c.mem.Set(key, it)
}

func (c *Cache) GetObjectWithExpiration(key string, obj interface{}, ttl int, f LoadFunc) error {
	v, ok := c.mem.Get(key)
	if ok {
		if v.Outdated() {
			to := deepcopy.Copy(obj)
			go c.syncMem(key, to, ttl, f)
		}
		return clone(v.Object, obj)
	}

	v, ok = c.rds.Get(key, obj)
	if ok {
		if v.Outdated() {
			go c.rds.load(key, nil, ttl, c.lazyMode, f, false)
		}
		return clone(v.Object, obj)
	}
	return c.rds.load(key, obj, ttl, c.lazyMode, f, true)
}

func (c *Cache) GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	return c.GetObjectWithExpiration(key, obj, ttl, f)
}

// notify all cache nodes to delete key
func (c *Cache) Delete(key string) error {
	return RedisPublish(delKeyChannel, key, c.pool)
}

// redis subscriber for key deletion
func (c *Cache) subscribe(key string) error {
	conn := c.pool.Get()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(key); err != nil {
		return err
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			key := string(v.Data)
			c.delete(key)
		case error:
			return v
		}
	}
}

func (c *Cache) delete(keyPattern string) error {
	c.mem.Delete(keyPattern)
	return c.rds.Delete(keyPattern)
}

// clone object to return, to avoid dirty data
func clone(src, dst interface{}) error {
	if reflect.TypeOf(src) != reflect.TypeOf(dst) {
		return fmt.Errorf("inconsistent type, [%+v] expected, but got [%+v]", reflect.TypeOf(dst), reflect.TypeOf(src))
	}

	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
	return nil
}
