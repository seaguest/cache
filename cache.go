package cache

import (
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
	// redis connection, handle Pushlish/Subscribe when delete a key
	pool *redis.Pool

	// rds cache, handle redis level cache
	rds *RedisCache

	// mem cache, handle memory cache operation
	mem *MemCache

	// indicate if cache is in lazy mode
	// when lazy is true, keys will stay longer in mem and redis
	lazy bool
}

type LoadFunc func() (interface{}, error)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func New(redisAddr, redisPassword string, lazy bool, maxConnection int) *Cache {
	c := &Cache{}
	c.pool = GetRedisPool(redisAddr, redisPassword, maxConnection)
	c.mem = NewMemCache(time.Second * 2)
	c.rds = NewRedisCache(c.pool, c.mem)
	c.lazy = lazy

	// subscribe key deletion
	go c.subscribe(delKeyChannel)
	return c
}

// load mem memcache from redis
func (c *Cache) updateMem(key string, copy interface{}, ttl int, f LoadFunc) {
	it, ok := c.rds.Get(key, copy)
	// if key not exists in redis or data outdated
	if !ok || it.Outdated() {
		c.rds.load(key, nil, ttl, c.lazy, f, false)
		return
	}
	c.mem.Set(key, it)
}

func (c *Cache) GetObjectWithExpiration(key string, obj interface{}, ttl int, f LoadFunc) error {
	v, ok := c.mem.Get(key)
	if ok {
		if v.Outdated() {
			to := deepcopy.Copy(obj)
			go c.updateMem(key, to, ttl, f)
		}
		clone(v.Object, obj)
		return nil
	}

	v, ok = c.rds.Get(key, obj)
	if ok {
		if v.Outdated() {
			go c.rds.load(key, nil, ttl, c.lazy, f, false)
		}
		clone(v.Object, obj)
		return nil
	}
	return c.rds.load(key, obj, ttl, c.lazy, f, true)
}

func (c *Cache) GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	return c.GetObjectWithExpiration(key, obj, ttl, f)
}

// notify all other cache to delete key
func (c *Cache) Delete(key string) error {
	return RedisPublish(delKeyChannel, key, c.pool)
}

// subscriber delete key from mem and redis
func (c *Cache) subscribe(key string) error {
	conn := c.pool.Get()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(key); err != nil {
		return err
	}

	go func() {
		for {
			switch v := psc.Receive().(type) {
			case redis.Message:
				key := string(v.Data)
				c.delete(key)
			}
		}
	}()
	return nil
}

func (c *Cache) delete(keyPattern string) error {
	// remove memcache
	c.mem.Delete(keyPattern)

	// remove redis key
	return c.rds.Delete(keyPattern)
}

// clone object to return, to avoid dirty data
func clone(src, dst interface{}) {
	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
}
