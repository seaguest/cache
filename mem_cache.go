// inspired by https://github.com/patrickmn/go-cache
package cache

import (
	"runtime"
	"sync"
	"time"
)

type MemCache struct {
	*memcache
}

type memcache struct {
	items   sync.Map
	janitor *janitor
}

// memcache will scan all objects per clean interval, expired key will be
// deleted.
func NewMemCache(ci time.Duration) *MemCache {
	c := &memcache{
		items: sync.Map{},
	}
	C := &MemCache{c}
	if ci > 0 {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

func (c *memcache) Set(k string, it *Item) {
	c.items.Store(k, it)
}

// check value exists and up-to-date
func (c *memcache) UpToDate(k string) (*Item, bool) {
	it, exists := c.Get(k)
	if !exists {
		return nil, false
	}
	return it, !it.Outdated()
}

// Get an item from the memcache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *memcache) Get(k string) (*Item, bool) {
	tmp, found := c.items.Load(k)
	if !found {
		return nil, false
	}
	item := tmp.(*Item)
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item, true
}

// Delete an item from the memcache. Does nothing if the key is not in the memcache.
func (c *memcache) Delete(k string) {
	c.delete(k)
}

func (c *memcache) delete(k string) (interface{}, bool) {
	c.items.Delete(k)
	return nil, false
}

// Delete all expired items from the memcache.
func (c *memcache) DeleteExpired() {
	now := time.Now().UnixNano()
	c.items.Range(func(key, value interface{}) bool {
		v := value.(*Item)
		k := key.(string)
		if v.Expiration > 0 && now > v.Expiration {
			c.delete(k)
		}
		return true
	})
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *memcache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *MemCache) {
	c.janitor.stop <- true
}

func runJanitor(c *memcache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)
}
