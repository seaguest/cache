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
	ErrIllegalTTL         = errors.New("illegal ttl, must be superior than second")
	ErrUnknownObjectType  = errors.New("unknown object_type")
	ErrConflictObjectType = errors.New("object_type conflict")
)

const (
	keyNotificationChannel = "cache_key_notification_channel"
	cleanInterval          = time.Second * 10 // default memory cache clean interval
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
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.GetObjectType == nil {
		cfg.GetObjectType = func(key string) string {
			return strings.Split(key, ":")[0]
		}
	}
	if cfg.OnError == nil {
		panic("must provide OnError function")
	}
	c.cfg = cfg
	c.mem = NewMemCache(cleanInterval)
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
		err = ctx.Err()
	}
	return errors.WithStack(err)
}

func (c *Cache) setObject(key string, obj interface{}, ttl time.Duration) error {
	if ttl > ttl.Truncate(time.Second) {
		return errors.WithStack(ErrIllegalTTL)
	}
	ttlInSeconds := int(ttl / time.Second)

	objectTypeName := c.cfg.GetObjectType(key)
	// maintain the type mapping
	objectType, ok := c.objectTypes.Load(objectTypeName)
	if !ok {
		c.objectTypes.Store(objectTypeName, &ObjectType{Type: obj, TTL: ttlInSeconds})
	} else {
		// check registered and current type, must be coherent
		if reflect.TypeOf(objectType.(*ObjectType).Type) != reflect.TypeOf(obj) {
			return errors.WithStack(ErrConflictObjectType)
		}
	}

	key = c.getKey(key)
	_, err, _ := c.sfg.Do(key, func() (interface{}, error) {
		dst := deepcopy.Copy(obj)
		it := newItem(obj, ttlInSeconds)
		c.mem.set(key, it)

		err := c.rds.set(objectTypeName, key, dst, ttlInSeconds)
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
		err = ctx.Err()
	}
	return errors.WithStack(err)
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
		return ErrIllegalTTL
	}
	ttlInSeconds := int(ttl / time.Second)

	var metricType MetricType
	defer c.report(metricType, c.cfg.GetObjectType(key))()

	objectTypeName := c.cfg.GetObjectType(key)
	// maintain the type mapping
	objectType, ok := c.objectTypes.Load(objectTypeName)
	if !ok {
		c.objectTypes.Store(objectTypeName, &ObjectType{Type: obj, TTL: ttlInSeconds})
	} else {
		// check registered and current type, must be coherent
		if reflect.TypeOf(objectType.(*ObjectType).Type) != reflect.TypeOf(obj) {
			return errors.WithStack(ErrConflictObjectType)
		}
	}

	key = c.getKey(key)
	v := c.mem.get(key)
	if v != nil {
		metricType = MetricTypeMemHit
		if v.ExpireAt != 0 && v.ExpireAt < time.Now().Unix() {
			metricType = MetricTypeMemHitExpired
			go func() {
				it, err := c.rds.load(objectTypeName, key, ttlInSeconds, f)
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
					it, err := c.rds.load(objectTypeName, key, ttlInSeconds, f)
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
		it, err := c.rds.load(objectTypeName, key, ttlInSeconds, f)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		c.notify(notificationTypeSet, objectTypeName, key, it.Object)
		return it, err
	})
	if err != nil {
		return errors.WithStack(err)
	}

	it := itf.(*Item)
	return c.copy(it.Object, obj)
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
			var msg NotificationMessage
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

				obj := deepcopy.Copy(objType.(*ObjectType).Type)
				if err := json.Unmarshal([]byte(msg.Payload), obj); err != nil {
					c.cfg.OnError(errors.WithStack(err))
					continue
				}

				it := newItem(obj, objType.(*ObjectType).TTL)
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
	return fmt.Sprintf("%s:%s", c.cfg.Namespace, keyNotificationChannel)
}

func (c *Cache) notify(notificationType notificationType, objectType string, key string, obj interface{}) {
	bs, _ := json.Marshal(obj)

	// publish message
	msg := &NotificationMessage{
		NotificationType: notificationType,
		ObjectType:       objectType,
		Key:              key,
		Payload:          string(bs),
	}

	msgBody, _ := json.Marshal(msg)
	publish(c.getNotifyChannel(), string(msgBody), c.cfg.GetConn())
}
