package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

func getRedisPool(address, password string, maxConnection int) *redis.Pool {
	pool := &redis.Pool{
		MaxIdle:     maxConnection,
		MaxActive:   maxConnection,
		Wait:        false,
		IdleTimeout: 240 * time.Second,
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
		Dial: func() (redis.Conn, error) {
			return dial("tcp", address, password)
		},
	}
	return pool
}

func dial(network, address, password string) (redis.Conn, error) {
	c, err := redis.Dial(network, address)
	if err != nil {
		return nil, err
	}
	if password != "" {
		if _, err := c.Do("AUTH", password); err != nil {
			c.Close()
			return nil, err
		}
	}
	return c, err
}

func setString(key, value string, ttl int, pool *redis.Pool) error {
	conn := pool.Get()
	defer conn.Close()

	if err := conn.Err(); err != nil {
		return err
	}
	_, err := conn.Do("SETEX", key, ttl, value)
	return err
}

func getString(key string, pool *redis.Pool) (string, error) {
	conn := pool.Get()
	defer conn.Close()

	if err := conn.Err(); err != nil {
		return "", err
	}

	s, err := redis.String(conn.Do("GET", key))
	if err != nil {
		return "", err
	}
	return s, nil
}

func delete(key string, pool *redis.Pool) error {
	conn := pool.Get()
	defer conn.Close()

	var err error
	if err = conn.Err(); err != nil {
		return err
	}

	_, err = conn.Do("DEL", key)
	return err
}

func publish(channel, message string, pool *redis.Pool) error {
	conn := pool.Get()
	defer conn.Close()

	if err := conn.Err(); err != nil {
		return err
	}

	_, err := conn.Do("PUBLISH", channel, message)
	if err != nil {
		return err
	}
	return nil
}
