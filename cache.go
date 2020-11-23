package cache

import (
	"errors"
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
	cleanInterval = time.Second * 10
)

type Cache struct {
	// redis connection
	pool *redis.Pool

	// rds cache, handles redis level cache
	rds *RedisCache

	// mem cache, handles in-memory cache
	mem *MemCache

	// in debug mode, ttl is set to 1s, mem clean interval is set to 1s
	debug bool
}

type LoadFunc func() (interface{}, error)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func New(redisAddr, redisPassword string, maxConnection int) *Cache {
	c := &Cache{}
	c.pool = GetRedisPool(redisAddr, redisPassword, maxConnection)
	c.mem = NewMemCache(cleanInterval)
	c.rds = NewRedisCache(c.pool, c.mem)

	// subscribe key deletion
	go c.subscribe(delKeyChannel)
	return c
}

// enable debug mode
func (c *Cache) EnableDebug() {
	c.debug = true
	c.mem.StopScan()
	c.mem.SetCleanInterval(time.Second)
	c.mem.StartScan()
}

// sync memcache from redis
func (c *Cache) syncMem(key string, copy interface{}, ttl int, f LoadFunc) {
	it, ok := c.rds.Get(key, copy)
	// if key not exists in redis or data outdated, then load from redis
	if !ok || it.Outdated() {
		c.rds.load(key, nil, ttl, f, false)
		return
	}
	c.mem.Set(key, it)
}

func (c *Cache) getObjectWithExpiration(key string, obj interface{}, ttl int, f LoadFunc) error {
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
			go c.rds.load(key, nil, ttl, f, false)
		}
		return clone(v.Object, obj)
	}
	return c.rds.load(key, obj, ttl, f, true)
}

func (c *Cache) GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	// is debug is enabled, set all ttl to 1s, clean interval to 1s
	if c.debug {
		ttl = 1
	}
	return c.getObjectWithExpiration(key, obj, ttl, f)
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
func clone(src, dst interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprint(r))
			return
		}
	}()

	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
	return
}
