package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gomodule/redigo/redis"
)

// RedisStore adapts redigo connections. The callback should normally be
// redis.Pool.Get; the store does not own or close the pool itself.
type RedisStore struct {
	getConn func() redis.Conn
}

func NewRedisStore(getConn func() redis.Conn) (*RedisStore, error) {
	if getConn == nil {
		return nil, fmt.Errorf("%w: nil Redis connection callback", ErrInvalidConfig)
	}
	return &RedisStore{getConn: getConn}, nil
}

func (s *RedisStore) conn() (redis.Conn, error) {
	conn := s.getConn()
	if conn == nil {
		return nil, errors.New("cache: Redis connection callback returned nil")
	}
	return conn, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, error) {
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	value, err := redis.Bytes(redis.DoContext(conn, ctx, "GET", key))
	if errors.Is(err, redis.ErrNil) {
		return nil, ErrNotFound
	}
	return value, err
}

func (s *RedisStore) MGet(ctx context.Context, keys ...string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	values, err := redis.Values(redis.DoContext(conn, ctx, "MGET", args...))
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		data, err := redis.Bytes(value, nil)
		if err != nil {
			return nil, err
		}
		out[keys[i]] = data
	}
	return out, nil
}

func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < time.Millisecond {
		return ErrInvalidTTL
	}
	conn, err := s.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = redis.DoContext(conn, ctx, "SET", key, value, "PX", ttl.Milliseconds())
	return err
}

func (s *RedisStore) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	args := make([]any, len(keys))
	for i, key := range keys {
		args[i] = key
	}
	conn, err := s.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = redis.DoContext(conn, ctx, "DEL", args...)
	return err
}

func (s *RedisStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	out := make(map[string]int64, len(names))
	if len(names) == 0 {
		return out, nil
	}
	args := make([]any, len(names))
	for i, name := range names {
		args[i] = name
	}
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	values, err := redis.Values(redis.DoContext(conn, ctx, "MGET", args...))
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		version, err := redis.Int64(value, nil)
		if err != nil {
			return nil, err
		}
		out[names[i]] = version
	}
	return out, nil
}

func (s *RedisStore) BumpVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	out := make(map[string]int64, len(names))
	if len(names) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(names)+2)
	args = append(args, bumpVersionsScript, len(names))
	for _, name := range names {
		args = append(args, name)
	}
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	values, err := redis.Int64s(redis.DoContext(conn, ctx, "EVAL", args...))
	if err != nil {
		return nil, err
	}
	if len(values) != len(names) {
		return nil, errors.New("cache: Redis returned an invalid generation response")
	}
	for i, name := range names {
		out[name] = values[i]
	}
	return out, nil
}

const bumpVersionsScript = `
local result = {}
for i, key in ipairs(KEYS) do
  result[i] = redis.call("INCR", key)
end
return result
`

func (s *RedisStore) Publish(ctx context.Context, channel string, event Invalidation) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	conn, err := s.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = redis.DoContext(conn, ctx, "PUBLISH", channel, body)
	return err
}

func (s *RedisStore) Subscribe(ctx context.Context, channel string, handle func(Invalidation)) error {
	conn, err := s.conn()
	if err != nil {
		return err
	}
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
	pubsub := redis.PubSubConn{Conn: conn}
	if err := pubsub.Subscribe(channel); err != nil {
		return err
	}
	for {
		switch message := pubsub.Receive().(type) {
		case redis.Message:
			var event Invalidation
			if json.Unmarshal(message.Data, &event) == nil {
				handle(event)
			}
		case error:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return message
		}
	}
}

func (s *RedisStore) TryLock(ctx context.Context, name string, ttl time.Duration) (string, bool, error) {
	if ttl < time.Millisecond {
		return "", false, ErrInvalidTTL
	}
	token, err := lockToken()
	if err != nil {
		return "", false, err
	}
	conn, err := s.conn()
	if err != nil {
		return "", false, err
	}
	defer conn.Close()
	reply, err := redis.String(redis.DoContext(conn, ctx, "SET", name, token, "NX", "PX", ttl.Milliseconds()))
	if errors.Is(err, redis.ErrNil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, reply == "OK", nil
}

func (s *RedisStore) Unlock(ctx context.Context, name, token string) error {
	conn, err := s.conn()
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = redis.DoContext(conn, ctx, "EVAL", unlockScript, 1, name, token)
	return err
}

const unlockScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`

func (s *RedisStore) Close() error { return nil }
