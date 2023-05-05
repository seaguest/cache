package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type redisCache struct {
	getConn func() redis.Conn

	redisTTLFactor int
}

func newRedisCache(getConn func() redis.Conn, redisTTLFactor int) *redisCache {
	return &redisCache{
		getConn:        getConn,
		redisTTLFactor: redisTTLFactor,
	}
}

// read item from redis
func (c *redisCache) get(key string, obj interface{}) (*Item, error) {
	body, err := c.getString(key)
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
		redisTTL = int(ttl/time.Second) * c.redisTTLFactor
	}

	bs, err := json.Marshal(it)
	if err != nil {
		return nil, err
	}

	err = c.setString(key, string(bs), redisTTL)
	if err != nil {
		return nil, err
	}
	return it, nil
}

func (c *redisCache) delete(key string) (err error) {
	conn := c.getConn()
	defer func() {
		if err == nil {
			err = conn.Close()
		}
	}()

	_, err = conn.Do("DEL", key)
	return
}

func (c *redisCache) setString(key, value string, ttl int) (err error) {
	conn := c.getConn()
	defer func() {
		if err == nil {
			err = conn.Close()
		}
	}()

	if ttl == 0 {
		_, err = conn.Do("SET", key, value)
	} else {
		_, err = conn.Do("SETEX", key, ttl, value)
	}
	return
}

func (c *redisCache) getString(key string) (value string, err error) {
	conn := c.getConn()
	defer func() {
		if err == nil {
			err = conn.Close()
		}
	}()

	value, err = redis.String(conn.Do("GET", key))
	return
}

func (c *redisCache) flush(prefix string) (err error) {
	conn := c.getConn()
	defer func() {
		if err == nil {
			err = conn.Close()
		}
	}()

	keys, err := redis.Strings(conn.Do("KEYS", prefix+"*"))
	if err != nil {
		return
	}

	for _, key := range keys {
		_, err = conn.Do("DEL", key)
	}
	return
}
