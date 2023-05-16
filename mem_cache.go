package cache

import (
	"sync"
	"time"
)

type memCache struct {
	items sync.Map
	ci    time.Duration
}

// newMemCache memcache will scan all objects for every clean interval and delete expired key.
func newMemCache(ci time.Duration) *memCache {
	c := &memCache{
		items: sync.Map{},
		ci:    ci,
	}

	go c.runJanitor()
	return c
}

// get an item from the memcache. Returns the item or nil, and a bool indicating whether the key was found.
func (c *memCache) get(k string) *Item {
	tmp, ok := c.items.Load(k)
	if !ok {
		return nil
	}

	return tmp.(*Item)
}

func (c *memCache) set(k string, it *Item) {
	c.items.Store(k, it)
}

// Delete an item from the memcache. Does nothing if the key is not in the memcache.
func (c *memCache) delete(k string) {
	c.items.Delete(k)
}

// start key scanning to delete expired keys
func (c *memCache) runJanitor() {
	ticker := time.NewTicker(c.ci)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		}
	}
}

// DeleteExpired delete all expired items from the memcache.
func (c *memCache) DeleteExpired() {
	c.items.Range(func(key, value interface{}) bool {
		v := value.(*Item)
		k := key.(string)
		// delete outdated for memory cache
		if v.ExpireAt != 0 && v.ExpireAt < time.Now().Unix() {
			c.items.Delete(k)
		}
		return true
	})
}
