package tiercache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gomodule/redigo/redis"
)

type RedisStore struct {
	getConn func() redis.Conn
	channel string
}

// NewRedisStore returns a Redis-backed Store using redigo connections.
func NewRedisStore(getConn func() redis.Conn, namespace string) *RedisStore {
	if namespace == "" {
		namespace = "tiercache"
	}
	return &RedisStore{
		getConn: getConn,
		channel: namespace + ":invalidate",
	}
}

func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn := s.getConn()
	defer conn.Close()
	b, err := redis.Bytes(conn.Do("GET", key))
	if errors.Is(err, redis.ErrNil) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *RedisStore) MGet(ctx context.Context, keys ...string) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	conn := s.getConn()
	defer conn.Close()
	values, err := redis.Values(conn.Do("MGET", args...))
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		b, err := redis.Bytes(value, nil)
		if err != nil {
			return nil, err
		}
		out[keys[i]] = b
	}
	return out, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn := s.getConn()
	defer conn.Close()
	if ttl <= 0 {
		_, err := conn.Do("SET", key, value)
		return err
	}
	_, err := conn.Do("SET", key, value, "PX", ttl.Milliseconds())
	return err
}

func (s *RedisStore) Delete(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	conn := s.getConn()
	defer conn.Close()
	_, err := conn.Do("DEL", args...)
	return err
}

func (s *RedisStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	if len(names) == 0 {
		return out, nil
	}
	args := make([]any, len(names))
	for i, name := range names {
		args[i] = name
	}
	conn := s.getConn()
	defer conn.Close()
	values, err := redis.Values(conn.Do("MGET", args...))
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			out[names[i]] = 0
			continue
		}
		v, err := redis.Int64(value, nil)
		if err != nil {
			return nil, err
		}
		out[names[i]] = v
	}
	return out, nil
}

func (s *RedisStore) BumpVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	if len(names) == 0 {
		return out, nil
	}
	conn := s.getConn()
	defer conn.Close()
	for _, name := range names {
		if err := conn.Send("INCR", name); err != nil {
			return nil, err
		}
	}
	if err := conn.Flush(); err != nil {
		return nil, err
	}
	for _, name := range names {
		v, err := redis.Int64(conn.Receive())
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

func (s *RedisStore) ExpireVersions(ctx context.Context, ttl time.Duration, names ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 || len(names) == 0 {
		return nil
	}
	conn := s.getConn()
	defer conn.Close()
	for _, name := range names {
		if err := conn.Send("PEXPIRE", name, ttl.Milliseconds()); err != nil {
			return err
		}
	}
	if err := conn.Flush(); err != nil {
		return err
	}
	for range names {
		if _, err := conn.Receive(); err != nil {
			return err
		}
	}
	return nil
}

func (s *RedisStore) Publish(ctx context.Context, event Invalidation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	conn := s.getConn()
	defer conn.Close()
	_, err = conn.Do("PUBLISH", s.channel, body)
	return err
}

func (s *RedisStore) Subscribe(ctx context.Context, handle func(Invalidation)) error {
	conn := s.getConn()
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(s.channel); err != nil {
		return err
	}
	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			var event Invalidation
			if err := json.Unmarshal(v.Data, &event); err == nil {
				handle(event)
			}
		case error:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return v
		}
	}
}

func (s *RedisStore) TryLock(ctx context.Context, name string, ttl time.Duration) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	token, err := lockToken()
	if err != nil {
		return "", false, err
	}
	conn := s.getConn()
	defer conn.Close()
	reply, err := redis.String(conn.Do("SET", name, token, "NX", "PX", ttl.Milliseconds()))
	if errors.Is(err, redis.ErrNil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, reply == "OK", nil
}

func (s *RedisStore) Unlock(ctx context.Context, name, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn := s.getConn()
	defer conn.Close()
	const unlockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`
	_, err := conn.Do("EVAL", unlockScript, 1, name, token)
	return err
}

func (s *RedisStore) Close() error { return nil }
