package cache

/*
Cache Library with Structured Logging Support

This package provides a two-tier caching system with Redis and in-memory storage.
Debug logging can be enabled using the DebugLog option to log cache operations.

Example usage with debug logging:
	c := cache.New(
		cache.GetConn(redisPool.Get),
		cache.Namespace("myapp"),
		cache.Separator("#"),
		cache.DebugLog(true), // Enable debug logging
		cache.OnError(func(ctx context.Context, err error) {
			log.Printf("Cache error: %+v", err)
		}),
	)
*/

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pkg/errors"
	"github.com/seaguest/deepcopy"
	"golang.org/x/sync/singleflight"
)

var (
	ErrIllegalTTL = errors.New("illegal ttl, must be in whole numbers of seconds, no fractions")
)

const (
	defaultNamespace = "default"
)

type Cache interface {
	// GetObject loader function f() will be called in case cache all miss
	// suggest to use object_type#id as key or any other pattern which can easily extract object, aggregate metric for same object in onMetric
	GetObject(ctx context.Context, key string, obj any, ttl time.Duration, f func() (any, error), opts ...Option) error

	Delete(ctx context.Context, key string) error

	// DeleteFromMem allows to delete key from mem, for test purpose
	DeleteFromMem(key string)

	// DeleteFromRedis allows to delete key from redis, for test purpose
	DeleteFromRedis(key string) error
}

type cache struct {
	options Options

	// rds cache, handles redis level cache
	rds *redisCache

	// mem cache, handles in-memory cache
	mem *memCache

	sfg singleflight.Group

	metric Metrics

	logger *slog.Logger
}

func New(options ...Option) Cache {
	c := &cache{}
	opts := newOptions(options...)

	// set default namespace if missing
	if opts.Namespace == "" {
		opts.Namespace = defaultNamespace
	}

	// set separator
	if opts.Separator == "" {
		panic("Separator unspecified")
	}

	// set default RedisTTLFactor to 4 if missing
	if opts.RedisTTLFactor == 0 {
		opts.RedisTTLFactor = 4
	}

	// if get policy is not specified, use returnExpired policy, return data even if data is expired.
	if opts.GetPolicy == 0 {
		opts.GetPolicy = GetPolicyReturnExpired
	}

	// set default CleanInterval to 10s if missing
	if opts.CleanInterval == 0 {
		opts.CleanInterval = time.Second * 10
	} else if opts.CleanInterval < time.Second {
		panic("CleanInterval must be second at least")
	}

	if opts.OnError == nil {
		panic("OnError is nil")
	}

	c.options = opts
	c.metric = opts.Metric
	c.metric.namespace = opts.Namespace
	c.metric.separator = opts.Separator
	c.mem = newMemCache(opts.CleanInterval, c.metric)
	c.rds = newRedisCache(opts.GetConn, opts.RedisTTLFactor, c.metric)
	go c.watchDelete()

	// Set up logger based on debug option
	var logLevel slog.Leveler = slog.LevelInfo
	if opts.DebugLog {
		logLevel = slog.LevelDebug
	}
	c.logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	return c
}

func (c *cache) GetObject(ctx context.Context, key string, obj any, ttl time.Duration, f func() (any, error), opts ...Option) error {
	opt := newOptions(opts...)

	c.logger.Debug("[seaguest/cache] GetObject called", "key", key, "ttl", ttl)
	defer c.logger.Debug("[seaguest/cache] GetObject completed", "key", key, "ttl", ttl, "obj", obj)

	// is disabled, call loader function
	if c.options.Disabled {
		o, err := f()
		if err != nil {
			return err
		}
		return c.copy(ctx, o, obj)
	}

	done := make(chan error)
	var err error
	go func() {
		done <- c.getObject(ctx, key, obj, ttl, f, opt)
	}()

	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.WithStack(ctx.Err())
	}
	return err
}

func (c *cache) getObject(ctx context.Context, key string, obj any, ttl time.Duration, f func() (any, error), opt Options) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	var expired bool
	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeGetCache, &err)

	// use GetCachePolicy from inout if provided, otherwise take from global options.
	getPolicy := opt.GetPolicy
	if getPolicy == 0 {
		getPolicy = c.options.GetPolicy
	}

	var it *Item
	defer func() {
		if expired && getPolicy == GetPolicyReloadOnExpiry {
			it, err = c.resetObject(ctx, namespacedKey, ttl, f, opt)
		}
		// deepcopy before return
		if err == nil {
			err = c.copy(ctx, it.Object, obj)
		}

		// if expired and get policy is not ReloadOnExpiry, then do a async load.
		if expired && getPolicy != GetPolicyReloadOnExpiry {
			go func() {
				// async load metric
				defer c.metric.Observe()(namespacedKey, MetricTypeAsyncLoad, nil)

				_, resetErr := c.resetObject(ctx, namespacedKey, ttl, f, opt)
				if resetErr != nil {
					c.options.OnError(ctx, errors.WithStack(resetErr))
					return
				}
			}()
		}
	}()

	// try to retrieve from local cache, return if found
	it = c.mem.get(namespacedKey)
	if it != nil {
		if it.Expired() {
			expired = true
		}
		return
	}

	var itf interface{}
	itf, err, _ = c.sfg.Do(namespacedKey+"_get", func() (interface{}, error) {
		// try to retrieve from redis, return if found
		v, redisErr := c.rds.get(namespacedKey, obj)
		if redisErr != nil {
			return nil, errors.WithStack(redisErr)
		}
		if v != nil {
			if v.Expired() {
				expired = true
			} else {
				// update memory cache since it is not previously found in mem
				c.mem.set(namespacedKey, v)
			}
			return v, nil
		}
		return c.resetObject(ctx, namespacedKey, ttl, f, opt)
	})
	if err != nil {
		return
	}
	it = itf.(*Item)
	return
}

// resetObject load fresh data to redis and in-memory with loader function
func (c *cache) resetObject(ctx context.Context, namespacedKey string, ttl time.Duration, f func() (any, error), opt Options) (*Item, error) {
	itf, err, _ := c.sfg.Do(namespacedKey+"_reset", func() (it interface{}, err error) {
		// add metric for a fresh load
		defer c.metric.Observe()(namespacedKey, MetricTypeLoad, &err)

		defer func() {
			if r := recover(); r != nil {
				switch v := r.(type) {
				case error:
					err = errors.WithStack(v)
				default:
					err = errors.New(fmt.Sprint(r))
				}
				c.options.OnError(ctx, err)
			}
		}()

		var o interface{}
		o, err = f()
		if err != nil {
			return
		}

		// update local mem first
		c.mem.set(namespacedKey, newItem(o, ttl))

		it, err = c.rds.set(namespacedKey, o, ttl)
		if err != nil {
			return
		}
		return
	})
	if err != nil {
		return nil, err
	}
	return itf.(*Item), nil
}

func (c *cache) DeleteFromMem(key string) {
	namespacedKey := c.namespacedKey(key)
	c.mem.delete(namespacedKey)
}

func (c *cache) DeleteFromRedis(key string) error {
	namespacedKey := c.namespacedKey(key)
	return c.rds.delete(namespacedKey)
}

// Delete notify all cache instances to delete cache key
func (c *cache) Delete(ctx context.Context, key string) (err error) {
	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeDeleteCache, &err)

	// delete redis, then pub to delete cache
	if err = c.rds.delete(namespacedKey); err != nil {
		err = errors.WithStack(err)
		return
	}

	conn := c.options.GetConn()
	defer conn.Close()

	_, err = conn.Do("PUBLISH", c.deleteChannel(), key)
	if err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
	}
	return
}

// copy object to return, to avoid dirty data
func (c *cache) copy(ctx context.Context, src, dst any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case error:
				err = errors.WithStack(v)
			default:
				err = errors.New(fmt.Sprint(r))
			}
			c.options.OnError(ctx, err)
		}
		c.logger.Debug("[seaguest/cache] Copy completed", "src", src, "dst", dst, "err", err)
	}()

	err = deepcopy.CopyTo(src, dst)
	if err != nil {
		return errors.WithStack(err)
	}
	return nil
}

func (c *cache) namespacedKey(key string) string {
	return c.options.Namespace + ":" + key
}

func (c *cache) deleteChannel() string {
	return c.options.Namespace + ":delete_channel"
}

// watchDelete watch the delete channel and delete the cache from mem
func (c *cache) watchDelete() {
	ctx := context.Background()
begin:
	conn := c.options.GetConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(c.deleteChannel()); err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			key := string(v.Data)
			c.mem.delete(key)
		case error:
			c.options.OnError(ctx, errors.WithStack(v))
			time.Sleep(time.Second) // Wait for a second before attempting to receive messages again
			if strings.Contains(v.Error(), "use of closed network connection") || strings.Contains(v.Error(), "connect: connection refused") {
				// if connection becomes invalid, then restart watch with new conn
				goto begin
			}
		}
	}
}
