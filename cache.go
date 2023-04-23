package cache

import (
	"context"
	"fmt"
	"log"
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
	SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration) error

	// GetObject loader function f() will be called in case cache all miss
	GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f func() (interface{}, error)) error

	Delete(key string) error

	// FlushMem clean all mem cache
	FlushMem() error

	// FlushRedis clean all redis cache
	FlushRedis() error

	// Disable GetObject will call loader function in case cache is disabled.
	Disable()
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
	c.mem = newMemCache(opts.CleanInterval)
	c.rds = newRedisCache(opts.GetConn, opts.RedisTTLFactor)
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

func (c *cache) setObject(key string, obj interface{}, ttl time.Duration) error {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

	key = c.namespacedKey(key)
	_, err, _ := c.sfg.Do(key+"set", func() (interface{}, error) {
		_, err := c.rds.set(key, obj, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notifyAll(&actionRequest{
			Action:   cacheSet,
			TypeName: typeName,
			Key:      key,
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

	start := time.Now()
	var metricType MetricType
	typeName := getTypeName(obj)
	c.checkType(typeName, obj, ttl)

	log.Println("002-------start")

	var it *Item
	defer func() {
		if c.options.OnMetric != nil {
			elapsedTime := time.Since(start)
			c.options.OnMetric(c.unNamespacedKey(key), metricType, elapsedTime)
		}

		// deepcopy before return
		if err == nil {
			err = c.copy(it.Object, obj)
		}

		// if hit but expired, then do a fresh load
		if metricType == MetricTypeMemHitExpired || metricType == MetricTypeRedisHitExpired {
			go func() {
				_, resetErr := c.resetObject(key, ttl, f)
				if resetErr != nil {
					c.options.OnError(errors.WithStack(resetErr))
					return
				}
			}()
		}
	}()

	key = c.namespacedKey(key)
	// try to retrieve from local cache, return if found
	it = c.mem.get(key)
	if it != nil {
		if !it.Expired() {
			metricType = MetricTypeMemHit
		} else {
			metricType = MetricTypeMemHitExpired
		}
		log.Println("003-------mem found-", metricType)
		return
	}

	var itf interface{}
	itf, err, _ = c.sfg.Do(key+"_get", func() (interface{}, error) {
		// try to retrieve from redis, return if found
		v, redisErr := c.rds.get(key, obj)
		if redisErr != nil {
			return nil, errors.WithStack(redisErr)
		}
		if v != nil {
			if !v.Expired() {
				metricType = MetricTypeRedisHit
				// update memory cache since it is not previously found in mem
				c.mem.set(key, v)
			} else {
				metricType = MetricTypeRedisHitExpired
			}
			log.Println("004-------redis found-", metricType)
			return v, nil
		}

		metricType = MetricTypeMiss
		log.Println("005-------loadfunc-", metricType)
		fmt.Println("2*************----", err)
		return c.resetObject(key, ttl, f)
	})
	log.Println("2-----------------", itf, err)

	if err != nil {
		return
	}
	it = itf.(*Item)
	return
}

// resetObject load fresh data to redis and in-memory with loader function
func (c *cache) resetObject(key string, ttl time.Duration, f func() (interface{}, error)) (*Item, error) {
	itf, err, _ := c.sfg.Do(key+"_reset", func() (it interface{}, err error) {
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

		it, err = c.rds.set(key, o, ttl)
		if err != nil {
			return
		}

		// notifyAll
		c.notifyAll(&actionRequest{
			Action:   cacheSet,
			TypeName: getTypeName(o),
			Key:      key,
			Object:   o,
		})
		return
	})
	log.Println("1-----------------", itf, err)
	if err != nil {
		return nil, err
	}
	return itf.(*Item), nil
}

func (c *cache) FlushMem() error {
	c.mem.Flush()
	return nil
}

func (c *cache) FlushRedis() error {
	c.rds.Flush(c.options.Namespace + ":")
	return nil
}

// Delete notify all cache instances to delete cache key
func (c *cache) Delete(key string) error {
	key = c.namespacedKey(key)

	// delete redis, then pub to delete cache
	if err := c.rds.delete(key); err != nil {
		return errors.WithStack(err)
	}

	c.notifyAll(&actionRequest{
		Action: cacheDelete,
		Key:    key,
	})
	return nil
}

// checkType register type if not exists.
func (c *cache) checkType(typeName string, obj interface{}, ttl time.Duration) {
	_, ok := c.types.Load(typeName)
	if !ok {
		c.types.Store(typeName, &objectType{typ: obj, ttl: ttl})
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

func (c *cache) unNamespacedKey(key string) string {
	return strings.TrimPrefix(key, c.options.Namespace+":")
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
	Action cacheAction `json:"actionRequest"`

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
			var msg actionRequest
			if err := json.Unmarshal(v.Data, &msg); err != nil {
				c.options.OnError(errors.WithStack(err))
				continue
			}
			c.onAction(&msg)
		case error:
			c.options.OnError(errors.WithStack(v))
		}
	}
}

func (c *cache) onAction(ar *actionRequest) {
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
}

// notifyAll will broadcast the cache change to all cache instances
func (c *cache) notifyAll(ar *actionRequest) {
	fmt.Println("notifyAll...", ar)

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
	defer func() {
		if err == nil {
			err = conn.Close()
			if err != nil {
				c.options.OnError(errors.WithStack(err))
			}
		}
	}()

	_, err = conn.Do("PUBLISH", c.actionChannel(), string(msgBody))
	if err != nil {
		c.options.OnError(errors.WithStack(err))
		return
	}
}
