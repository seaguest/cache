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

// store one mutex for each key
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

// 从redis读取item
func (c *RedisCache) Get(key string, obj interface{}) (*Item, bool) {
	mux := c.getMutex(key)
	mux.RLock()
	defer func() {
		mux.RUnlock()
	}()

	if v, valid := c.mem.UpToDate(key); valid {
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
	// 保存到mem
	c.mem.Set(key, &it)
	return &it, true
}

// 写入到redis
func (c *RedisCache) Set(key string, it *Item) {
	mux := c.getMutex(key)
	mux.Lock()
	defer func() {
		mux.Unlock()
	}()

	c.set(key, it)
}

// 写入到redis
func (c *RedisCache) set(key string, it *Item) error {
	ttl := (it.Expiration - time.Now().UnixNano()) / int64(time.Second)
	bs, _ := json.Marshal(it)
	return RedisSetString(key, string(bs), int(ttl), c.pool)
}

// 从db加载到redis，mem
func (c *RedisCache) load(key string, obj interface{}, ttl int, lazy bool, f LoadFunc, sync bool) error {
	mux := c.getMutex(key)
	mux.Lock()
	defer func() {
		mux.Unlock()
	}()

	if it, valid := c.mem.UpToDate(key); valid {
		if sync {
			clone(it.Object, obj)
		}
		return nil
	}

	o, err := f()
	if err != nil {
		return err
	}

	// 进当同步模式下，需要更新obj
	if sync {
		clone(o, obj)
	}

	// update memcache
	it := NewItem(o, ttl, lazy)

	// 更新redis
	c.set(key, it)

	// 更新mem
	c.mem.Set(key, it)
	return nil
}

func (c *RedisCache) Delete(keyPattern string) error {
	return RedisDelKey(keyPattern, c.pool)
}
