package tiercache

import (
	"sync"
	"time"
)

type localIndex struct {
	mu    sync.Mutex
	byKey map[Key]map[string]time.Time
	byTag map[Tag]map[string]time.Time
}

func newLocalIndex() *localIndex {
	return &localIndex{
		byKey: make(map[Key]map[string]time.Time),
		byTag: make(map[Tag]map[string]time.Time),
	}
}

func (idx *localIndex) add(key Key, tags []Tag, storageKey string, expiresAt time.Time) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	addIndexed(idx.byKey, key, storageKey, expiresAt)
	for _, tag := range tags {
		addIndexed(idx.byTag, tag, storageKey, expiresAt)
	}
}

func addIndexed[K comparable](m map[K]map[string]time.Time, k K, storageKey string, expiresAt time.Time) {
	keys := m[k]
	if keys == nil {
		keys = make(map[string]time.Time)
		m[k] = keys
	}
	keys[storageKey] = expiresAt
}

func (idx *localIndex) keysForKey(key Key, now time.Time) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return collectIndexed(idx.byKey, key, now)
}

func (idx *localIndex) keysForTag(tag Tag, now time.Time) []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	return collectIndexed(idx.byTag, tag, now)
}

func collectIndexed[K comparable](m map[K]map[string]time.Time, k K, now time.Time) []string {
	keys := m[k]
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for storageKey, expiresAt := range keys {
		if !expiresAt.IsZero() && now.After(expiresAt) {
			delete(keys, storageKey)
			continue
		}
		out = append(out, storageKey)
	}
	if len(keys) == 0 {
		delete(m, k)
	}
	return out
}

func (idx *localIndex) clear() {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.byKey = make(map[Key]map[string]time.Time)
	idx.byTag = make(map[Tag]map[string]time.Time)
}
