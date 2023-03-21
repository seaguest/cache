package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type redisCache struct {
	getConn func() redis.Conn

	redisFactor int
}

func newRedisCache(getConn func() redis.Conn, redisFactor int) *redisCache {
	return &redisCache{
		getConn:     getConn,
		redisFactor: redisFactor,
	}
}

// read item from redis
func (c *redisCache) get(key string, obj interface{}) (*Item, error) {
	body, err := getString(key, c.getConn())
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

func (c *redisCache) set(key string, obj interface{}, ttl time.Duration) (*Item, error) {
	it := newItem(obj, ttl)
	redisTTL := 0
	if ttl > 0 {
		redisTTL = int(ttl/time.Second) * c.redisFactor
	}

	bs, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}

	err = setString(key, string(bs), redisTTL, c.getConn())
	if err != nil {
		return nil, err
	}
	return it, nil
}

func (c *redisCache) delete(key string) error {
	return delete(key, c.getConn())
}
