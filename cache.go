package cache

import (
	"context"
	"fmt"
	"reflect"
	"sync"
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
	SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration) error

	// GetObject loader function f() will be called in case cache all miss
	// suggest to use object#id as key or any other pattern which can easily extract object, aggregate metric for same object in onMetric
	GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error)) error

	Delete(key string) error

	// Disable GetObject will call loader function in case cache is disabled.
	Disable()

	// DeleteFromMem allows to delete key from mem, for test purpose
	DeleteFromMem(key string)

	// DeleteFromRedis allows to delete key from redis, for test purpose
	DeleteFromRedis(key string) error
}

type cache struct {
	options Options

	// store the pkg_path+type<->object mapping
	types sync.Map

	// rds cache, handles redis level cache
	rds *redisCache

	// mem cache, handles in-memory cache
	mem *memCache

	sfg singleflight.Group

	metric Metrics
}

func New(options ...Option) Cache {
	c := &cache{}
	opts := newOptions(options...)

	// set default namespace if missing
	if opts.Namespace == "" {
		opts.Namespace = defaultNamespace
	}

	// set default RedisTTLFactor to 4 if missing
	if opts.RedisTTLFactor == 0 {
		opts.RedisTTLFactor = 4
	}

	// set default CleanInterval to 10s if missing
	if opts.CleanInterval == 0 {
		opts.CleanInterval = time.Second * 10
	} else if opts.CleanInterval < time.Second {
		panic("CleanInterval must be second at least")
	}

	if opts.OnError == nil {
		panic("need OnError for cache initialization")
	}

	c.options = opts
	c.metric = opts.Metric
	c.metric.namespace = opts.Namespace
	c.mem = newMemCache(opts.CleanInterval, c.metric)
	c.rds = newRedisCache(opts.GetConn, opts.RedisTTLFactor, c.metric)
	go c.watch()
	return c
}

// Disable , disable cache, call loader function for each call
func (c *cache) Disable() {
	c.options.Disabled = true
}

func (c *cache) SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration) error {
	done := make(chan error)
	var err error
	go func() {
		done <- c.setObject(key, obj, ttl)
	}()

	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.WithStack(ctx.Err())
	}
	return err
}

func (c *cache) setObject(key string, obj interface{}, ttl time.Duration) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeSetCache, &err)

	_, err, _ = c.sfg.Do(namespacedKey+"_set", func() (interface{}, error) {
		_, err := c.rds.set(namespacedKey, obj, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notifyAll(&actionRequest{
			Action:   cacheSet,
			TypeName: typeName,
			Key:      namespacedKey,
			Object:   obj,
		})
		return nil, nil
	})
	return err
}

func (c *cache) GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error)) error {
	// is disabled, call loader function
	if c.options.Disabled {
		o, err := f()
		if err != nil {
			return err
		}
		return c.copy(o, obj)
	}

	done := make(chan error)
	var err error
	go func() {
		done <- c.getObject(key, obj, ttl, f)
	}()

	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.WithStack(ctx.Err())
	}
	return err
}

func (c *cache) getObject(key string, obj interface{}, ttl time.Duration, f func() (interface{}, error)) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

	var expired bool
	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeGetCache, &err)

	var it *Item
	defer func() {
		// deepcopy before return
		if err == nil {
			err = c.copy(it.Object, obj)
		}

		// if hit but expired, then do a fresh load
		if expired {
			go func() {
				// async load metric
				defer c.metric.Observe()(namespacedKey, MetricTypeAsyncLoad, nil)

				_, resetErr := c.resetObject(namespacedKey, ttl, f)
				if resetErr != nil {
					c.options.OnError(errors.WithStack(resetErr))
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
		return c.resetObject(namespacedKey, ttl, f)
	})
	if err != nil {
		return
	}
	it = itf.(*Item)
	return
}

// resetObject load fresh data to redis and in-memory with loader function
func (c *cache) resetObject(namespacedKey string, ttl time.Duration, f func() (interface{}, error)) (*Item, error) {
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
				c.options.OnError(err)
			}
		}()

		var o interface{}
		o, err = f()
		if err != nil {
			return
		}

		it, err = c.rds.set(namespacedKey, o, ttl)
		if err != nil {
			return
		}

		// notifyAll
		c.notifyAll(&actionRequest{
			Action:   cacheSet,
			TypeName: getTypeName(o),
			Key:      namespacedKey,
			Object:   o,
		})
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
func (c *cache) Delete(key string) (err error) {
	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeDeleteCache, &err)

	// delete redis, then pub to delete cache
	if err = c.rds.delete(namespacedKey); err != nil {
		err = errors.WithStack(err)
		return
	}

	c.notifyAll(&actionRequest{
		Action: cacheDelete,
		Key:    namespacedKey,
	})
	return
}

// checkType register type if not exists.
func (c *cache) checkType(typeName string, obj interface{}, ttl time.Duration) {
	_, ok := c.types.Load(typeName)
	if !ok {
		c.types.Store(typeName, &objectType{typ: deepcopy.Copy(obj), ttl: ttl})
	}
}

// copy object to return, to avoid dirty data
func (c *cache) copy(src, dst interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case error:
				err = errors.WithStack(v)
			default:
				err = errors.New(fmt.Sprint(r))
			}
			c.options.OnError(err)
		}
	}()

	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
	return
}

// get the global unique id for a given object type.
func getTypeName(obj interface{}) string {
	return reflect.TypeOf(obj).Elem().PkgPath() + "/" + reflect.TypeOf(obj).String()
}

func (c *cache) namespacedKey(key string) string {
	return c.options.Namespace + ":" + key
}

type cacheAction int

const (
	cacheSet cacheAction = iota // cacheSet == 0
	cacheDelete
)

// stores the object type and its ttl in memory
type objectType struct {
	// object type
	typ interface{}

	// object ttl
	ttl time.Duration
}

// actionRequest defines an entity which will be broadcast to all cache instances
type actionRequest struct {
	// cacheSet or cacheDelete
	Action cacheAction `json:"action"`

	// the type_name of the target object
	TypeName string `json:"type_name"`

	// key of the cache item
	Key string `json:"key"`

	// object stored in the cache, only used by caller, won't be broadcast
	Object interface{} `json:"-"`

	// the marshaled string of object
	Payload []byte `json:"payload"`
}

func (c *cache) actionChannel() string {
	return c.options.Namespace + ":action_channel"
}

// watch the cache update
func (c *cache) watch() {
	conn := c.options.GetConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(c.actionChannel()); err != nil {
		c.options.OnError(errors.WithStack(err))
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			var ar actionRequest
			if err := json.Unmarshal(v.Data, &ar); err != nil {
				c.options.OnError(errors.WithStack(err))
				continue
			}

			switch ar.Action {
			case cacheSet:
				objType, ok := c.types.Load(ar.TypeName)
				if !ok {
					return
				}

				obj := deepcopy.Copy(objType.(*objectType).typ)
				if err := json.Unmarshal(ar.Payload, obj); err != nil {
					c.options.OnError(errors.WithStack(err))
					return
				}

				it := newItem(obj, objType.(*objectType).ttl)
				c.mem.set(ar.Key, it)
			case cacheDelete:
				c.mem.delete(ar.Key)
			}
		case error:
			c.options.OnError(errors.WithStack(v))
		}
	}
}

// notifyAll will broadcast the cache change to all cache instances
func (c *cache) notifyAll(ar *actionRequest) {
	bs, err := json.Marshal(ar.Object)
	if err != nil {
		c.options.OnError(errors.WithStack(err))
		return
	}
	ar.Payload = bs

	msgBody, err := json.Marshal(ar)
	if err != nil {
		c.options.OnError(errors.WithStack(err))
		return
	}

	conn := c.options.GetConn()
	defer conn.Close()

	_, err = conn.Do("PUBLISH", c.actionChannel(), string(msgBody))
	if err != nil {
		c.options.OnError(errors.WithStack(err))
		return
	}
}
