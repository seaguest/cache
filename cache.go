package cache

import (
	"context"
	"fmt"
	"reflect"
	"strings"
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
	SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, opts ...Option) error

	// GetObject loader function f() will be called in case cache all miss
	// suggest to use object_type#id as key or any other pattern which can easily extract object, aggregate metric for same object in onMetric
	GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error), opts ...Option) error

	Delete(ctx context.Context, key string) error

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
	// if update policy is not specified, use NoBroadcast policy, don't broadcast to other nodes when cache is updated.
	if opts.UpdatePolicy == 0 {
		opts.UpdatePolicy = UpdatePolicyNoBroadcast
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
	go c.watch()
	return c
}

// Disable , disable cache, call loader function for each call
func (c *cache) Disable() {
	c.options.Disabled = true
}

func (c *cache) SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, opts ...Option) error {
	opt := newOptions(opts...)
	done := make(chan error)
	var err error
	go func() {
		done <- c.setObject(ctx, key, obj, ttl, opt)
	}()

	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.WithStack(ctx.Err())
	}
	return err
}

func (c *cache) setObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, opt Options) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	// use UpdateCachePolicy from inout if provided, otherwise take from global options.
	updatePolicy := opt.UpdatePolicy
	if updatePolicy == 0 {
		updatePolicy = c.options.UpdatePolicy
	}

	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeSetCache, &err)

	_, err, _ = c.sfg.Do(namespacedKey+"_set", func() (interface{}, error) {
		// update local mem first
		c.mem.set(namespacedKey, newItem(obj, ttl))

		_, err := c.rds.set(namespacedKey, obj, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notifyAll(
			ctx,
			&actionRequest{
				Action:       cacheSet,
				TypeName:     typeName,
				Key:          namespacedKey,
				Object:       obj,
				UpdatePolicy: updatePolicy,
			})
		return nil, nil
	})
	return err
}

func (c *cache) GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error), opts ...Option) error {
	opt := newOptions(opts...)

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

func (c *cache) getObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error), opt Options) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

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
func (c *cache) resetObject(ctx context.Context, namespacedKey string, ttl time.Duration, f func() (interface{}, error), opt Options) (*Item, error) {
	itf, err, _ := c.sfg.Do(namespacedKey+"_reset", func() (it interface{}, err error) {
		// use UpdateCachePolicy from inout if provided, otherwise take from global options.
		updatePolicy := opt.UpdatePolicy
		if updatePolicy == 0 {
			updatePolicy = c.options.UpdatePolicy
		}

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

		// notifyAll
		c.notifyAll(
			ctx,
			&actionRequest{
				Action:       cacheSet,
				TypeName:     getTypeName(o),
				Key:          namespacedKey,
				Object:       o,
				UpdatePolicy: updatePolicy,
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
func (c *cache) Delete(ctx context.Context, key string) (err error) {
	namespacedKey := c.namespacedKey(key)
	defer c.metric.Observe()(namespacedKey, MetricTypeDeleteCache, &err)

	// delete redis, then pub to delete cache
	if err = c.rds.delete(namespacedKey); err != nil {
		err = errors.WithStack(err)
		return
	}

	c.notifyAll(
		ctx,
		&actionRequest{
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
func (c *cache) copy(ctx context.Context, src, dst interface{}) (err error) {
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
	cacheSet cacheAction = iota + 1 // cacheSet == 1
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

	// update policy, if NoBroadcast, then won't PUBLISH in set case.
	UpdatePolicy UpdateCachePolicy `json:"update_policy"`
}

func (c *cache) actionChannel() string {
	return c.options.Namespace + ":action_channel"
}

// watch the cache update
func (c *cache) watch() {
	ctx := context.Background()
begin:
	conn := c.options.GetConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(c.actionChannel()); err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			var ar actionRequest
			if err := unmarshal(v.Data, &ar); err != nil {
				c.options.OnError(ctx, errors.WithStack(err))
				continue
			}

			switch ar.Action {
			case cacheSet:
				objType, ok := c.types.Load(ar.TypeName)
				if !ok {
					continue
				}

				obj := deepcopy.Copy(objType.(*objectType).typ)
				if err := unmarshal(ar.Payload, obj); err != nil {
					c.options.OnError(ctx, errors.WithStack(err))
					continue
				}

				it := newItem(obj, objType.(*objectType).ttl)
				it.Size = len(ar.Payload)
				c.mem.set(ar.Key, it)
			case cacheDelete:
				c.mem.delete(ar.Key)
			}
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

// notifyAll will broadcast the cache change to all cache instances
func (c *cache) notifyAll(ctx context.Context, ar *actionRequest) {
	// if update policy is NoBroadcast, don't broadcast for set.
	if ar.UpdatePolicy == UpdatePolicyNoBroadcast && ar.Action == cacheSet {
		return
	}

	bs, err := marshal(ar.Object)
	if err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
		return
	}
	ar.Payload = bs

	msgBody, err := marshal(ar)
	if err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
		return
	}

	conn := c.options.GetConn()
	defer conn.Close()

	_, err = conn.Do("PUBLISH", c.actionChannel(), string(msgBody))
	if err != nil {
		c.options.OnError(ctx, errors.WithStack(err))
		return
	}
}
