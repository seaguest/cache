package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

type redisCache struct {
	// func to get redis conn from pool
	getConn func() redis.Conn

	// TTL in redis will be redisTTLFactor*mem_ttl
	redisTTLFactor int

	// metric for redis cache
	metric Metrics
}

func newRedisCache(getConn func() redis.Conn, redisTTLFactor int, metric Metrics) *redisCache {
	return &redisCache{
		getConn:        getConn,
		redisTTLFactor: redisTTLFactor,
		metric:         metric,
	}
}

// read item from redis
func (c *redisCache) get(key string, obj interface{}) (it *Item, err error) {
	var metricType string
	defer c.metric.Observe()(key, &metricType, &err)

	body, err := c.getString(key)
	if err != nil {
		if err == redis.ErrNil {
			metricType = MetricTypeGetRedisMiss
			err = nil
			return
		} else {
			return
		}
	}

	it = &Item{}
	it.Object = obj
	err = unmarshal([]byte(body), it)
	if err != nil {
		return
	}

	if !it.Expired() {
		metricType = MetricTypeGetRedisHit
	} else {
		metricType = MetricTypeGetRedisExpired
	}
	it.Size = len(body)
	return
}

func (c *redisCache) set(key string, obj interface{}, ttl time.Duration) (it *Item, err error) {
	// redis set
	defer c.metric.Observe()(key, MetricTypeSetRedis, &err)

	it = newItem(obj, ttl)
	redisTTL := 0
	if ttl > 0 {
		redisTTL = int(ttl/time.Second) * c.redisTTLFactor
	}

	bs, err := marshal(it)
	if err != nil {
		return
	}

	err = c.setString(key, string(bs), redisTTL)
	if err != nil {
		return
	}
	return
}

func (c *redisCache) delete(key string) (err error) {
	// redis del
	defer c.metric.Observe()(key, MetricTypeDeleteRedis, &err)

	conn := c.getConn()
	defer conn.Close()

	_, err = conn.Do("DEL", key)
	return
}

func (c *redisCache) setString(key, value string, ttl int) (err error) {
	conn := c.getConn()
	defer conn.Close()

	if ttl == 0 {
		_, err = conn.Do("SET", key, value)
	} else {
		_, err = conn.Do("SETEX", key, ttl, value)
	}
	return
}

func (c *redisCache) getString(key string) (value string, err error) {
	conn := c.getConn()
	defer conn.Close()

	value, err = redis.String(conn.Do("GET", key))
	return
}
