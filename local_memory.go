package cache

import (
	"container/list"
	"sync"
	"time"
)

const localShardCount = 64

type localCache struct {
	shards [localShardCount]localShard
	closed bool
	mu     sync.RWMutex
}

type localShard struct {
	mu      sync.Mutex
	maxCost int64
	cost    int64
	items   map[string]*list.Element
	lru     list.List
}

type localEntry struct {
	key       string
	value     []byte
	cost      int64
	expiresAt time.Time
}

// NewLocalCache returns a bounded, sharded LRU cache. maxCost is an approximate
// byte budget including keys, values, and entry overhead.
func NewLocalCache(maxCost int64) LocalCache {
	if maxCost <= 0 {
		maxCost = 64 << 20
	}
	cache := &localCache{}
	perShard := maxCost / localShardCount
	if perShard < 1 {
		perShard = 1
	}
	for i := range cache.shards {
		cache.shards[i].maxCost = perShard
		cache.shards[i].items = make(map[string]*list.Element)
	}
	return cache
}

func (c *localCache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, false
	}
	shard := &c.shards[localHash(key)&(localShardCount-1)]
	shard.mu.Lock()
	c.mu.RUnlock()
	defer shard.mu.Unlock()
	element := shard.items[key]
	if element == nil {
		return nil, false
	}
	entry := element.Value.(*localEntry)
	if !entry.expiresAt.IsZero() && !time.Now().Before(entry.expiresAt) {
		shard.remove(element)
		return nil, false
	}
	shard.lru.MoveToFront(element)
	return entry.value, true
}

func (c *localCache) Set(key string, value []byte, cost int64, ttl time.Duration) bool {
	if ttl < time.Millisecond {
		return false
	}
	if cost <= 0 {
		cost = int64(len(value))
	}
	cost += int64(len(key)) + 64
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return false
	}
	shard := &c.shards[localHash(key)&(localShardCount-1)]
	shard.mu.Lock()
	c.mu.RUnlock()
	defer shard.mu.Unlock()
	if cost > shard.maxCost {
		return false
	}
	if element := shard.items[key]; element != nil {
		shard.remove(element)
	}
	entry := &localEntry{
		key:       key,
		value:     append([]byte(nil), value...),
		cost:      cost,
		expiresAt: time.Now().Add(ttl),
	}
	shard.items[key] = shard.lru.PushFront(entry)
	shard.cost += cost
	for shard.cost > shard.maxCost {
		shard.remove(shard.lru.Back())
	}
	return true
}

func (c *localCache) Delete(keys ...string) {
	for _, key := range keys {
		shard := &c.shards[localHash(key)&(localShardCount-1)]
		shard.mu.Lock()
		if element := shard.items[key]; element != nil {
			shard.remove(element)
		}
		shard.mu.Unlock()
	}
}

func (c *localCache) Clear() {
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		shard.items = make(map[string]*list.Element)
		shard.lru.Init()
		shard.cost = 0
		shard.mu.Unlock()
	}
}

func (c *localCache) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.Clear()
	return nil
}

func (s *localShard) remove(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*localEntry)
	delete(s.items, entry.key)
	s.cost -= entry.cost
	s.lru.Remove(element)
}

func localHash(key string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	hash := uint64(offset)
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= prime
	}
	return hash
}

type noopLocal struct{}

// NewNoopLocal disables L1 storage.
func NewNoopLocal() LocalCache { return noopLocal{} }

func (noopLocal) Get(string) ([]byte, bool)                     { return nil, false }
func (noopLocal) Set(string, []byte, int64, time.Duration) bool { return false }
func (noopLocal) Delete(...string)                              {}
func (noopLocal) Clear()                                        {}
func (noopLocal) Close() error                                  { return nil }
