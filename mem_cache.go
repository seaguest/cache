package cache

import (
	"strings"
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
		metricType = MetricTypeGetMemHit
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

type memStat struct {
	count    int
	memUsage int
}

// DeleteExpired delete all expired items from the memcache.
func (c *memCache) DeleteExpired() {
	ms := make(map[string]*memStat)
	c.items.Range(func(key, value interface{}) bool {
		v := value.(*Item)
		k := key.(string)

		objectType := strings.Split(strings.TrimPrefix(k, c.metric.namespace+":"), c.metric.separator)[0]
		stat, ok := ms[objectType]
		if !ok {
			stat = &memStat{
				count:    1,
				memUsage: v.Size,
			}
		} else {
			stat.count += 1
			stat.memUsage += v.Size
		}
		ms[objectType] = stat

		// delete outdated for memory cache
		if v.Expired() {
			c.items.Delete(k)
		}
		return true
	})

	for k, v := range ms {
		c.metric.Set(k, MetricTypeCount, v.count)
		c.metric.Set(k, MetricTypeMemUsage, v.memUsage)
	}
}
