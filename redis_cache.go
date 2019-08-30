package cache

import (
	"sync"
	"time"

	"github.com/gomodule/redigo/redis"
)

type RedisCache struct {
	pool *redis.Pool
	muxm sync.Map // RWMutex map for each key
	mem  *MemCache
}

func NewRedisCache(p *redis.Pool, m *MemCache) *RedisCache {
	c := &RedisCache{
		pool: p,
		mem:  m,
	}
	return c
}

// store one mutex per key
// TODO, mux should be freed when no more accessed
func (c *RedisCache) getMutex(key string) *sync.RWMutex {
	var mux *sync.RWMutex
	nMux := new(sync.RWMutex)
	if oMux, ok := c.muxm.LoadOrStore(key, nMux); ok {
		mux = oMux.(*sync.RWMutex)
		nMux = nil
	} else {
		mux = nMux
	}
	return mux
}

// read item from redis
func (c *RedisCache) Get(key string, obj interface{}) (*Item, bool) {
	mux := c.getMutex(key)
	mux.RLock()
	defer func() {
		mux.RUnlock()
	}()

	// check if item is fresh in mem, return directly
	if v, fresh := c.mem.Load(key); fresh {
		return v, true
	}

	body, err := RedisGetString(key, c.pool)
	if err != nil && err != redis.ErrNil {
		return nil, false
	}
	if body == "" {
		return nil, false
	}

	var it Item
	it.Object = obj
	err = json.Unmarshal([]byte(body), &it)
	if err != nil {
		return nil, false
	}
	c.mem.Set(key, &it)
	return &it, true
}

// load redis with loader function
// when sync is true, obj must be set before return.
func (c *RedisCache) load(key string, obj interface{}, ttl int, lazyMode bool, f LoadFunc, sync bool) error {
	mux := c.getMutex(key)
	mux.Lock()
	defer func() {
		mux.Unlock()
	}()

	if it, fresh := c.mem.Load(key); fresh {
		if sync {
			clone(it.Object, obj)
		}
		return nil
	}

	o, err := f()
	if err != nil {
		return err
	}

	if sync {
		clone(o, obj)
	}

	// update memcache
	it := NewItem(o, ttl, lazyMode)

	rdsTTL := (it.Expiration - time.Now().UnixNano()) / int64(time.Second)
	bs, _ := json.Marshal(it)
	err = RedisSetString(key, string(bs), int(rdsTTL), c.pool)
	if err != nil {
		return err
	}

	c.mem.Set(key, it)
	return nil
}

func (c *RedisCache) Delete(keyPattern string) error {
	return RedisDelKey(keyPattern, c.pool)
}
