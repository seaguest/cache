package tiercache

import (
	"container/list"
	"sync"
	"time"
)

type memoryLocal struct {
	mu      sync.Mutex
	maxCost int64
	cost    int64
	items   map[string]*list.Element
	lru     *list.List
	closed  bool
}

type memoryLocalItem struct {
	key       string
	value     []byte
	cost      int64
	expiresAt time.Time
}

// NewMemoryLocal returns an in-memory LRU local cache for tests and small
// deployments.
func NewMemoryLocal(maxCost int64) LocalCache {
	if maxCost <= 0 {
		maxCost = 64 << 20
	}
	return &memoryLocal{
		maxCost: maxCost,
		items:   make(map[string]*list.Element),
		lru:     list.New(),
	}
}

func (l *memoryLocal) Get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, false
	}
	elem := l.items[key]
	if elem == nil {
		return nil, false
	}
	item := elem.Value.(*memoryLocalItem)
	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		l.remove(elem)
		return nil, false
	}
	l.lru.MoveToFront(elem)
	return append([]byte(nil), item.value...), true
}

func (l *memoryLocal) Set(key string, value []byte, cost int64, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	if cost <= 0 {
		cost = int64(len(value))
	}
	if cost > l.maxCost {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	if elem := l.items[key]; elem != nil {
		l.remove(elem)
	}
	item := &memoryLocalItem{
		key:       key,
		value:     append([]byte(nil), value...),
		cost:      cost,
		expiresAt: time.Now().Add(ttl),
	}
	elem := l.lru.PushFront(item)
	l.items[key] = elem
	l.cost += cost
	for l.cost > l.maxCost {
		elem := l.lru.Back()
		if elem == nil {
			break
		}
		l.remove(elem)
	}
	return true
}

func (l *memoryLocal) Delete(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, key := range keys {
		if elem := l.items[key]; elem != nil {
			l.remove(elem)
		}
	}
}

func (l *memoryLocal) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = make(map[string]*list.Element)
	l.lru.Init()
	l.cost = 0
}

func (l *memoryLocal) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.items = nil
	l.lru.Init()
	l.cost = 0
	return nil
}

func (l *memoryLocal) remove(elem *list.Element) {
	item := elem.Value.(*memoryLocalItem)
	delete(l.items, item.key)
	l.cost -= item.cost
	l.lru.Remove(elem)
}

type noopLocal struct{}

// NewNoopLocal returns a local cache adapter that stores nothing.
func NewNoopLocal() LocalCache { return noopLocal{} }

func (noopLocal) Get(string) ([]byte, bool)                     { return nil, false }
func (noopLocal) Set(string, []byte, int64, time.Duration) bool { return false }
func (noopLocal) Delete(...string)                              {}
func (noopLocal) Clear()                                        {}
func (noopLocal) Close() error                                  { return nil }
