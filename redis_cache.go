package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
	jsoniter "github.com/json-iterator/go"
	"golang.org/x/sync/singleflight"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type RedisCache struct {
	cfg Config

	mem *MemCache

	sfg singleflight.Group
}

func NewRedisCache(cfg Config, mem *MemCache) *RedisCache {
	return &RedisCache{
		cfg: cfg,
		mem: mem,
	}
}

// read item from redis
func (c *RedisCache) get(key string, obj interface{}) (*Item, error) {
	body, err := getString(key, c.cfg.GetConn())
	if err != nil {
		if err == redis.ErrNil {
			return nil, nil
		} else {
			return nil, err
		}
	}

	var it Item
	it.Object = obj
	err = json.Unmarshal([]byte(body), &it)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// read item from redis
func (c *RedisCache) set(key string, obj interface{}, ttl time.Duration) error {
	bs, _ := json.Marshal(obj)
	return setString(key, string(bs), int(ttl/time.Second), c.cfg.GetConn())
}

// load redis with loader function
// when sync is true, obj must be set before return.
func (c *RedisCache) load(key string, ttl time.Duration, f LoadFunc) (*Item, error) {
	loadingKey := key + "_loading"
	itf, err, _ := c.sfg.Do(loadingKey, func() (interface{}, error) {
		o, err := f()
		if err != nil {
			return nil, err
		}

		it := newItem(o, ttl)
		redisTTL := 0
		if ttl > 0 {
			redisTTL = int(ttl/time.Second) * c.cfg.RedisFactor
		}

		bs, _ := json.Marshal(it)
		err = setString(key, string(bs), redisTTL, c.cfg.GetConn())
		if err != nil {
			return nil, err
		}

		// update mem cache
		c.mem.set(key, it)

		return it, nil
	})
	return itf.(*Item), err
}

func (c *RedisCache) delete(key string) error {
	return delete(key, c.cfg.GetConn())
}
