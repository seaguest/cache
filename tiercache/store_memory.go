package tiercache

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu             sync.RWMutex
	data           map[string]memoryStoreItem
	versions       map[string]int64
	versionExpires map[string]time.Time
	locks          map[string]memoryLock
	subs           map[chan Invalidation]struct{}
	closed         bool
}

type memoryStoreItem struct {
	value     []byte
	expiresAt time.Time
}

type memoryLock struct {
	token     string
	expiresAt time.Time
}

// NewMemoryStore returns an in-memory Store for tests and local development.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data:           make(map[string]memoryStoreItem),
		versions:       make(map[string]int64),
		versionExpires: make(map[string]time.Time),
		locks:          make(map[string]memoryLock),
		subs:           make(map[chan Invalidation]struct{}),
	}
}

func (s *MemoryStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	if !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
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
	for _, key := range keys {
		item, ok := s.data[key]
		if !ok {
			continue
		}
		if !item.expiresAt.IsZero() && now.After(item.expiresAt) {
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
	expiresAt := time.Time{}
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = memoryStoreItem{value: append([]byte(nil), value...), expiresAt: expiresAt}
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, keys ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		delete(s.data, key)
	}
	return nil
}

func (s *MemoryStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(names))
	for _, name := range names {
		if expiresAt := s.versionExpires[name]; !expiresAt.IsZero() && time.Now().After(expiresAt) {
			delete(s.versions, name)
			delete(s.versionExpires, name)
		}
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
	out := make(map[string]int64, len(names))
	for _, name := range names {
		s.versions[name]++
		out[name] = s.versions[name]
	}
	return out, nil
}

func (s *MemoryStore) ExpireVersions(ctx context.Context, ttl time.Duration, names ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ttl <= 0 {
		return nil
	}
	expiresAt := time.Now().Add(ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		s.versionExpires[name] = expiresAt
	}
	return nil
}

func (s *MemoryStore) Publish(ctx context.Context, event Invalidation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	subs := make([]chan Invalidation, 0, len(s.subs))
	for ch := range s.subs {
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

func (s *MemoryStore) TryLock(ctx context.Context, name string, ttl time.Duration) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if ttl <= 0 {
		ttl = time.Second
	}
	token, err := lockToken()
	if err != nil {
		return "", false, err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.locks[name]; ok && now.Before(cur.expiresAt) {
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
	if cur, ok := s.locks[name]; ok && cur.token == token {
		delete(s.locks, name)
	}
	return nil
}

func (s *MemoryStore) Subscribe(ctx context.Context, handle func(Invalidation)) error {
	ch := make(chan Invalidation, 32)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		close(ch)
		s.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-ch:
			handle(event)
		}
	}
}

func (s *MemoryStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.data = nil
	s.versions = nil
	s.versionExpires = nil
	s.locks = nil
	return nil
}

func (s *MemoryStore) DumpRecord(key string) (record, bool) {
	s.mu.RLock()
	item, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return record{}, false
	}
	r, err := decodeRecord(item.value)
	if err != nil {
		return record{}, false
	}
	return r, true
}
