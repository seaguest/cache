package cache

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is a deterministic in-memory Store useful in tests and local
// development. It is not a distributed replacement for Redis.
type MemoryStore struct {
	mu       sync.RWMutex
	data     map[string]memoryStoreItem
	versions map[string]int64
	locks    map[string]memoryLock
	subs     map[string]map[chan Invalidation]struct{}
	closed   bool
	closedCh chan struct{}
}

type memoryStoreItem struct {
	value     []byte
	expiresAt time.Time
}

type memoryLock struct {
	token     string
	expiresAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data:     make(map[string]memoryStoreItem),
		versions: make(map[string]int64),
		locks:    make(map[string]memoryLock),
		subs:     make(map[string]map[chan Invalidation]struct{}),
		closedCh: make(chan struct{}),
	}
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	item, ok := s.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	if !item.expiresAt.IsZero() && !time.Now().Before(item.expiresAt) {
		delete(s.data, key)
		return nil, ErrNotFound
	}
	return append([]byte(nil), item.value...), nil
}

func (s *MemoryStore) MGet(ctx context.Context, keys ...string) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	out := make(map[string][]byte, len(keys))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	for _, key := range keys {
		item, ok := s.data[key]
		if !ok {
			continue
		}
		if !item.expiresAt.IsZero() && !now.Before(item.expiresAt) {
			delete(s.data, key)
			continue
		}
		out[key] = append([]byte(nil), item.value...)
	}
	return out, nil
}

func (s *MemoryStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl < time.Millisecond {
		return ErrInvalidTTL
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.data[key] = memoryStoreItem{value: append([]byte(nil), value...), expiresAt: time.Now().Add(ttl)}
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	for _, key := range keys {
		delete(s.data, key)
	}
	return nil
}

func (s *MemoryStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	out := make(map[string]int64, len(names))
	for _, name := range names {
		out[name] = s.versions[name]
	}
	return out, nil
}

func (s *MemoryStore) BumpVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	out := make(map[string]int64, len(names))
	for _, name := range names {
		s.versions[name]++
		out[name] = s.versions[name]
	}
	return out, nil
}

func (s *MemoryStore) Publish(ctx context.Context, channel string, event Invalidation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrClosed
	}
	subs := make([]chan Invalidation, 0, len(s.subs[channel]))
	for ch := range s.subs[channel] {
		subs = append(subs, ch)
	}
	s.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
	return nil
}

func (s *MemoryStore) Subscribe(ctx context.Context, channel string, handle func(Invalidation)) error {
	ch := make(chan Invalidation, 32)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.subs[channel] == nil {
		s.subs[channel] = make(map[chan Invalidation]struct{})
	}
	s.subs[channel][ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs[channel], ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closedCh:
			return ErrClosed
		case event := <-ch:
			handle(event)
		}
	}
}

func (s *MemoryStore) TryLock(ctx context.Context, name string, ttl time.Duration) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	token, err := lockToken()
	if err != nil {
		return "", false, err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", false, ErrClosed
	}
	if current, ok := s.locks[name]; ok && now.Before(current.expiresAt) {
		return "", false, nil
	}
	s.locks[name] = memoryLock{token: token, expiresAt: now.Add(ttl)}
	return token, true, nil
}

func (s *MemoryStore) Unlock(ctx context.Context, name, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if current, ok := s.locks[name]; ok && current.token == token {
		delete(s.locks, name)
	}
	return nil
}

func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.closedCh)
	return nil
}
