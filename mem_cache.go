package cache

import (
	"sync"
	"time"
)

type memCache struct {
	// local cache
	items sync.Map

	// clean interval
	ci time.Duration

	// metric for mem cache
	metric Metrics
}

// newMemCache memcache will scan all objects for every clean interval and delete expired key.
func newMemCache(ci time.Duration, metric Metrics) *memCache {
	c := &memCache{
		items:  sync.Map{},
		ci:     ci,
		metric: metric,
	}

	go c.runJanitor()
	return c
}

// get an item from the memcache. Returns the item or nil, and a bool indicating whether the key was found.
func (c *memCache) get(key string) *Item {
	var metricType string
	defer c.metric.Observe()(key, &metricType, nil)

	tmp, ok := c.items.Load(key)
	if !ok {
		metricType = MetricTypeGetMemMiss
		return nil
	}

	it := tmp.(*Item)
	if !it.Expired() {
		metricType = MetricTypeGetMem
	} else {
		metricType = MetricTypeGetMemExpired
	}
	return it
}

func (c *memCache) set(key string, it *Item) {
	// mem set
	defer c.metric.Observe()(key, MetricTypeSetMem, nil)

	c.items.Store(key, it)
}

// Delete an item from the memcache. Does nothing if the key is not in the memcache.
func (c *memCache) delete(key string) {
	// mem del
	defer c.metric.Observe()(key, MetricTypeDeleteMem, nil)

	c.items.Delete(key)
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
		if v.Expired() {
			c.items.Delete(k)
		}
		return true
	})
}
