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
	ErrIllegalTTL         = errors.New("illegal ttl, must be in seconds at least")
	ErrUnknownObjectType  = errors.New("unknown object_type")
	ErrConflictObjectType = errors.New("object_type conflict")
)

type LoadFunc func() (interface{}, error)

type Cache struct {
	cfg Config

	// store the type<->object type mapping
	objectTypes sync.Map

	// rds cache, handles redis level cache
	rds *RedisCache

	// mem cache, handles in-memory cache
	mem *MemCache

	sfg singleflight.Group
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
	c.mem = NewMemCache(cfg.CleanInterval)
	c.rds = NewRedisCache(c.cfg, c.mem)

	// subscribe key deletion
	go c.subscribe(c.getNotifyChannel())
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

	objectTypeName := getObjectTypeName(obj)
	err := c.checkObjectType(objectTypeName, obj, ttl)
	if err != nil {
		return err
	}

	key = c.getKey(key)
	_, err, _ = c.sfg.Do(key, func() (interface{}, error) {
		dst := deepcopy.Copy(obj)
		it := newItem(obj, ttl)
		c.mem.set(key, it)

		err := c.rds.set(key, dst, ttl)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		c.notify(notificationTypeSet, objectTypeName, key, obj)
		return nil, nil
	})
	return err
}

func (c *Cache) GetObject(ctx context.Context, key string, obj interface{}, ttl time.Duration, f LoadFunc) error {
	// is disabled, call loader function
	if c.cfg.Disabled {
		o, err := f()
		if err != nil {
			return errors.WithStack(err)
		}
		return errors.WithStack(c.copy(o, obj))
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

func (c *Cache) report(metricType MetricType, objectType string) func() {
	start := time.Now()
	return func() {
		elapsedInMillis := time.Since(start).Milliseconds()
		if c.cfg.OnMetric != nil {
			c.cfg.OnMetric(metricType, objectType, int(elapsedInMillis))
		}
	}
}

func (c *Cache) getObject(key string, obj interface{}, ttl time.Duration, f LoadFunc) error {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}

	objectTypeName := getObjectTypeName(obj)
	var metricType MetricType
	defer c.report(metricType, objectTypeName)()

	err := c.checkObjectType(objectTypeName, obj, ttl)
	if err != nil {
		return err
	}

	key = c.getKey(key)
	v := c.mem.get(key)
	if v != nil {
		metricType = MetricTypeMemHit
		if v.ExpireAt != 0 && v.ExpireAt < time.Now().Unix() {
			metricType = MetricTypeMemHitExpired
			go func() {
				it, err := c.rds.load(key, ttl, f)
				if err != nil {
					c.cfg.OnError(errors.WithStack(err))
					return
				}
				c.notify(notificationTypeSet, objectTypeName, key, it.Object)
			}()
		}
		return c.copy(v.Object, obj)
	}

	itf, err, _ := c.sfg.Do(key, func() (interface{}, error) {
		v, err := c.rds.get(key, obj)
		if v != nil {
			metricType = MetricTypeRedisHit
			if v.ExpireAt != 0 && v.ExpireAt < time.Now().Unix() {
				metricType = MetricTypeRedisHitExpired
				go func() {
					it, err := c.rds.load(key, ttl, f)
					if err != nil {
						c.cfg.OnError(errors.WithStack(err))
						return
					}
					c.notify(notificationTypeSet, objectTypeName, key, it.Object)
				}()
			}
			return v, nil
		}

		metricType = MetricTypeMiss
		it, err := c.rds.load(key, ttl, f)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notify(notificationTypeSet, objectTypeName, key, it.Object)
		return it, nil
	})
	if err != nil {
		return errors.WithStack(err)
	}

	it := itf.(*Item)
	return c.copy(it.Object, obj)
}

// checkObjectType register objectType or check if existing objectType match the current one.
func (c *Cache) checkObjectType(objectTypeName string, obj interface{}, ttl time.Duration) error {
	objectTyp, ok := c.objectTypes.Load(objectTypeName)
	if !ok {
		c.objectTypes.Store(objectTypeName, &objectType{Type: obj, TTL: ttl})
	} else {
		// check registered and current type, must be coherent
		if reflect.TypeOf(objectTyp.(*objectType).Type) != reflect.TypeOf(obj) {
			return errors.WithStack(ErrConflictObjectType)
		}
	}
	return nil
}

// Delete notify all cache instances to delete cache key
func (c *Cache) Delete(objectType, key string) error {
	// delete redis, then pub to delete cache
	if err := c.rds.delete(key); err != nil {
		return errors.WithStack(err)
	}

	c.notify(notificationTypeSet, objectType, key, nil)
	return nil
}

// redis subscriber for key deletion, delete keys in memory
func (c *Cache) subscribe(channel string) {
	conn := c.cfg.GetConn()
	defer conn.Close()

	psc := redis.PubSubConn{Conn: conn}
	if err := psc.Subscribe(channel); err != nil {
		c.cfg.OnError(errors.WithStack(err))
		return
	}

	for {
		switch v := psc.Receive().(type) {
		case redis.Message:
			var msg notificationMessage
			if err := json.Unmarshal(v.Data, &msg); err != nil {
				c.cfg.OnError(errors.WithStack(err))
				continue
			}

			switch msg.NotificationType {
			case notificationTypeSet:
				objType, ok := c.objectTypes.Load(msg.ObjectType)
				if !ok {
					c.cfg.OnError(errors.WithStack(ErrUnknownObjectType))
					continue
				}

				obj := deepcopy.Copy(objType.(*objectType).Type)
				if err := json.Unmarshal([]byte(msg.Payload), obj); err != nil {
					c.cfg.OnError(errors.WithStack(err))
					continue
				}

				it := newItem(obj, objType.(*objectType).TTL)
				c.mem.set(msg.Key, it)
			case notificationTypeDel:
				c.mem.delete(msg.Key)
			}
		case error:
			c.cfg.OnError(errors.WithStack(v))
		}
	}
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

func (c *Cache) getKey(key string) string {
	return fmt.Sprintf("%s:%s", c.cfg.Namespace, key)
}

func (c *Cache) getNotifyChannel() string {
	return fmt.Sprintf("%s:cache_key_notification_channel", c.cfg.Namespace)
}

func (c *Cache) notify(notificationType notificationType, objectType string, key string, obj interface{}) {
	bs, _ := json.Marshal(obj)

	// publish message
	msg := &notificationMessage{
		NotificationType: notificationType,
		ObjectType:       objectType,
		Key:              key,
		Payload:          string(bs),
	}

	msgBody, _ := json.Marshal(msg)
	publish(c.getNotifyChannel(), string(msgBody), c.cfg.GetConn())
}

// get the global unique id for a given object type.
func getObjectTypeName(obj interface{}) string {
	return fmt.Sprintf("%s/%s", reflect.TypeOf(obj).Elem().PkgPath(), reflect.TypeOf(obj).String())
}
