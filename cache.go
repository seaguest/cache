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
	ErrIllegalTTL              = errors.New("illegal ttl, must be in second/minute/hour")
	ErrObjectTypeNotRegistered = errors.New("object_type not registered")
)

type LoadFunc func() (interface{}, error)

type Cache struct {
	cfg Config

	// store the import path(pkg_path+type)<->object mapping
	objectTypes sync.Map

	// rds cache, handles redis level cache
	rds *redisCache

	// mem cache, handles in-memory cache
	mem *memCache

	sfg singleflight.Group
}

// stores the object type and its ttl in memory
type objectType struct {
	typ interface{}
	ttl time.Duration
}

func New(cfg Config) *Cache {
	c := &Cache{}

	// set default namespace if missing
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}

	// set default RedisFactor to 4 if missing
	if cfg.RedisFactor == 0 {
		cfg.RedisFactor = 4
	}

	// set default CleanInterval to 10s if missing
	if cfg.CleanInterval == 0 {
		cfg.CleanInterval = time.Second * 10
	} else if cfg.CleanInterval < time.Second {
		panic("CleanInterval must be second at least")
	}

	if cfg.OnError == nil {
		panic("need OnError for cache initialization")
	}
	c.cfg = cfg
	c.mem = newMemCache(cfg.CleanInterval)
	c.rds = newRedisCache(cfg.GetConn, cfg.RedisFactor)
	go c.watch()
	return c
}

// Disable , disable cache, call loader function for each call
func (c *Cache) Disable() {
	c.cfg.Disabled = true
}

func (c *Cache) SetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration) error {
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

func (c *Cache) setObject(key string, obj interface{}, ttl time.Duration) error {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	objType := getObjectType(obj)
	c.registerObjectType(objType, obj, ttl)

	key = c.getKey(key)
	_, err, _ := c.sfg.Do(key+"set", func() (interface{}, error) {
		_, err := c.rds.set(key, obj, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notify(&notification{
			Type:       notificationTypeSet,
			ObjectType: objType,
			Key:        key,
			Object:     obj,
		})
		return nil, nil
	})
	return err
}

func (c *Cache) GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f LoadFunc) error {
	// is disabled, call loader function
	if c.cfg.Disabled {
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

func (c *Cache) onMetric(metricType MetricType, objectType string) func() {
	start := time.Now()
	return func() {
		elapsedInMicros := time.Since(start).Microseconds()
		if c.cfg.OnMetric != nil {
			c.cfg.OnMetric(metricType, objectType, int(elapsedInMicros))
		}
	}
}

func (c *Cache) getObject(key string, obj interface{}, ttl time.Duration, f LoadFunc) (err error) {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	objType := getObjectType(obj)
	var metricType MetricType
	defer c.onMetric(metricType, objType)()
	c.registerObjectType(objType, obj, ttl)

	var it *Item
	defer func() {
		// deepcopy before return
		if err == nil {
			err = c.copy(it.Object, obj)
		}

		// if hit but expired, then do a fresh load
		if metricType == MetricTypeMemHitExpired || metricType == MetricTypeRedisHitExpired {
			go func() {
				_, resetErr := c.resetObject(key, ttl, f)
				if resetErr != nil {
					c.cfg.OnError(errors.WithStack(resetErr))
					return
				}
			}()
		}
	}()

	key = c.getKey(key)
	// try to retrieve from local cache, return if found
	it = c.mem.get(key)
	if it != nil {
		if !it.Expired() {
			metricType = MetricTypeMemHit
		} else {
			metricType = MetricTypeMemHitExpired
		}
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
			return v, nil
		}

		metricType = MetricTypeMiss
		return c.resetObject(key, ttl, f)
	})
	if err != nil {
		return
	}
	it = itf.(*Item)
	return
}

// resetObject load fresh data to redis and in-memory with loader function
func (c *Cache) resetObject(key string, ttl time.Duration, f LoadFunc) (*Item, error) {
	it, err, _ := c.sfg.Do(key+"_reset", func() (interface{}, error) {
		o, err := f()
		if err != nil {
			return nil, err
		}

		it, err := c.rds.set(key, o, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		// notify
		c.notify(&notification{
			Type:       notificationTypeSet,
			ObjectType: getObjectType(o),
			Key:        key,
			Object:     o,
		})

		return it, nil
	})
	if err != nil {
		return nil, err
	}
	return it.(*Item), nil
}

// registerObjectType register objectType or check if existing objectType match the current one.
func (c *Cache) registerObjectType(objectTypeName string, obj interface{}, ttl time.Duration) {
	_, ok := c.objectTypes.Load(objectTypeName)
	if !ok {
		c.objectTypes.Store(objectTypeName, &objectType{typ: obj, ttl: ttl})
	}
}

// Delete notify all cache instances to delete cache key
func (c *Cache) Delete(key string, obj interface{}) error {
	objType := getObjectType(obj)
	// delete redis, then pub to delete cache
	if err := c.rds.delete(key); err != nil {
		return errors.WithStack(err)
	}

	c.notify(&notification{
		Type:       notificationTypeDel,
		ObjectType: objType,
		Key:        key,
		Object:     nil,
	})
	return nil
}

// copy object to return, to avoid dirty data
func (c *Cache) copy(src, dst interface{}) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case error:
				err = errors.WithStack(v)
			default:
				err = errors.New(fmt.Sprint(r))
			}
			c.cfg.OnError(err)
		}
	}()

	v := deepcopy.Copy(src)
	if reflect.ValueOf(v).IsValid() {
		reflect.ValueOf(dst).Elem().Set(reflect.Indirect(reflect.ValueOf(v)))
	}
	return
}

// get the global unique id for a given object type.
func getObjectType(obj interface{}) string {
	return fmt.Sprintf("%s/%s", reflect.TypeOf(obj).Elem().PkgPath(), reflect.TypeOf(obj).String())
}

func (c *Cache) getKey(key string) string {
	return fmt.Sprintf("%s:%s", c.cfg.Namespace, key)
}

type notificationType int

const (
	notificationTypeSet notificationType = iota // notificationTypeSet == 0
	notificationTypeDel
)

type notification struct {
	Type       notificationType `json:"type"`
	ObjectType string           `json:"object_type"`
	Key        string           `json:"key"`
	Object     interface{}      `json:"-"`
	Payload    string           `json:"payload"`
}

func (c *Cache) getNotificationChannel() string {
	return fmt.Sprintf("%s:notification_channel", c.cfg.Namespace)
}

// watch the cache update
func (c *Cache) watch() {
	conn := c.cfg.GetConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(c.getNotificationChannel()); err != nil {
		c.cfg.OnError(errors.WithStack(err))
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			var msg notification
			if err := json.Unmarshal(v.Data, &msg); err != nil {
				c.cfg.OnError(errors.WithStack(err))
				continue
			}
			c.onNotification(&msg)
		case error:
			c.cfg.OnError(errors.WithStack(v))
		}
	}
}

func (c *Cache) onNotification(ntf *notification) {
	switch ntf.Type {
	case notificationTypeSet:
		objType, ok := c.objectTypes.Load(ntf.ObjectType)
		if !ok {
			c.cfg.OnError(errors.Wrapf(ErrObjectTypeNotRegistered, ntf.ObjectType))
			break
		}

		obj := deepcopy.Copy(objType.(*objectType).typ)
		if err := json.Unmarshal([]byte(ntf.Payload), obj); err != nil {
			c.cfg.OnError(errors.WithStack(err))
			break
		}

		it := newItem(obj, objType.(*objectType).ttl)
		c.mem.set(ntf.Key, it)
	case notificationTypeDel:
		c.mem.delete(ntf.Key)
	}
}

func (c *Cache) notify(ntf *notification) {
	bs, err := json.Marshal(ntf.Object)
	if err != nil {
		c.cfg.OnError(errors.WithStack(err))
		return
	}
	ntf.Payload = string(bs)

	msgBody, err := json.Marshal(ntf)
	if err != nil {
		c.cfg.OnError(errors.WithStack(err))
		return
	}

	conn := c.cfg.GetConn()
	defer func() {
		if err == nil {
			err = conn.Close()
			if err != nil {
				c.cfg.OnError(errors.WithStack(err))
			}
		}
	}()

	_, err = conn.Do("PUBLISH", c.getNotificationChannel(), string(msgBody))
	if err != nil {
		c.cfg.OnError(errors.WithStack(err))
		return
	}
}
