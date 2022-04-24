package cache

import (
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/seaguest/deepcopy"
)

const (
	delKeyChannel = "delkey"
	cleanInterval = time.Second * 10 // default memory cache clean interval
)

type LoadFunc func() (interface{}, error)

type Cache struct {
	// redis connection
	getConn func() redis.Conn

	// rds cache, handles redis level cache
	rds *RedisCache

	// mem cache, handles in-memory cache
	mem *MemCache

	// if disabled, GetObject call loader function directly
	disabled bool
}

func New(gc func() redis.Conn) *Cache {
	c := &Cache{}
	c.getConn = gc
	c.mem = NewMemCache(cleanInterval)
	c.rds = NewRedisCache(c.getConn, c.mem)

	// subscribe key deletion
	go c.subscribe(delKeyChannel)
	return c
}

// Disable , disable cache, call loader function for each call
func (c *Cache) Disable() {
	c.disabled = true
}

// sync memcache from redis
func (c *Cache) syncMem(key string, obj interface{}, ttl int, f LoadFunc) {
	it, ok := c.rds.get(key, obj)
	// if key not exists in redis or data outdated, then load from redis
	if !ok || it.Outdated() {
		c.rds.load(key, nil, ttl, f, false)
		return
	}
	c.mem.set(key, it)
}

func (c *Cache) GetObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	// is disabled, call loader function
	if c.disabled {
		o, err := f()
		if err != nil {
			return err
		}
		return copy(o, obj)
	}
	return c.getObject(key, obj, ttl, f)
}

func (c *Cache) getObject(key string, obj interface{}, ttl int, f LoadFunc) error {
	v, ok := c.mem.get(key)
	if ok {
		if v.Outdated() {
			dst := deepcopy.Copy(obj)
			go c.syncMem(key, dst, ttl, f)
		}
		return copy(v.Object, obj)
	}

	v, ok = c.rds.get(key, obj)
	if ok {
		if v.Outdated() {
			go c.rds.load(key, nil, ttl, f, false)
		}
		return copy(v.Object, obj)
	}
	return c.rds.load(key, obj, ttl, f, true)
}

// Delete notify all cache instances to delete cache key
func (c *Cache) Delete(key string) error {
	// delete redis, then pub to delete cache
	c.rds.delete(key)
	return publish(delKeyChannel, key, c.getConn())
}

// redis subscriber for key deletion, delete keys in memory
func (c *Cache) subscribe(key string) error {
	conn := c.getConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(key); err != nil {
		return err
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			key := string(v.Data)
			c.mem.delete(key)
		case error:
			return v
		}
	}
}

// copy object to return, to avoid dirty data
func copy(src, dst interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprint(r))
			debug.PrintStack()
			return
		}
	}()

	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
	return
}
