package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

func getRedisPool(addr string, opts ...redis.DialOption) (*redis.Pool, error) {
	pool := &redis.Pool{
		MaxIdle:     200,
		MaxActive:   200,
		Wait:        false,
		IdleTimeout: 240 * time.Second,
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", addr, opts...)
		},
	}
	return pool, nil
}

func setString(key, value string, ttl int, conn redis.Conn) error {
	defer conn.Close()

	var err error
	if err = conn.Err(); err != nil {
		return err
	}
	if ttl == 0 {
		_, err = conn.Do("SET", key, value)
	} else {
		_, err = conn.Do("SETEX", key, ttl, value)
	}
	return err
}

func getString(key string, conn redis.Conn) (string, error) {
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

func delete(key string, conn redis.Conn) error {
	defer conn.Close()

	var err error
	if err = conn.Err(); err != nil {
		return err
	}

	_, err = conn.Do("DEL", key)
	return err
}

func publish(channel, message string, conn redis.Conn) error {
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
