package tiercache

import (
	"context"
	"sync"
	"time"
)

type versions struct {
	Namespace int64
	Key       int64
	Tags      map[Tag]int64
}

type versionCache struct {
	mu  sync.RWMutex
	ttl time.Duration
	now func() time.Time
	m   map[string]cachedVersion
}

type cachedVersion struct {
	value     int64
	expiresAt time.Time
}

func newVersionCache(ttl time.Duration) *versionCache {
	return &versionCache{
		ttl: ttl,
		now: time.Now,
		m:   make(map[string]cachedVersion),
	}
}

func (vc *versionCache) get(name string) (int64, bool) {
	if vc.ttl <= 0 {
		return 0, false
	}
	vc.mu.RLock()
	v, ok := vc.m[name]
	vc.mu.RUnlock()
	if !ok || vc.now().After(v.expiresAt) {
		return 0, false
	}
	return v.value, true
}

func (vc *versionCache) set(name string, value int64) {
	if vc.ttl <= 0 {
		return
	}
	vc.mu.Lock()
	vc.m[name] = cachedVersion{value: value, expiresAt: vc.now().Add(vc.ttl)}
	vc.mu.Unlock()
}

func (vc *versionCache) clear() {
	vc.mu.Lock()
	vc.m = make(map[string]cachedVersion)
	vc.mu.Unlock()
}

func (c *Client) versionNames(key Key, tags []Tag) []string {
	names := make([]string, 0, 2+len(tags))
	names = append(names, c.namespaceVersionName())
	names = append(names, c.keyVersionName(key))
	for _, tag := range tags {
		names = append(names, c.tagVersionName(tag))
	}
	return names
}

func (c *Client) namespaceVersionName() string {
	return c.namespace + ":v:namespace"
}

func (c *Client) keyVersionName(key Key) string {
	return c.namespace + ":v:key:" + hashHex([]byte(key))
}

func (c *Client) tagVersionName(tag Tag) string {
	return c.namespace + ":v:tag:" + hashHex([]byte(tag))
}

func (c *Client) loadVersions(ctx context.Context, key Key, tags []Tag, consistency Consistency) (versions, error) {
	names := c.versionNames(key, tags)
	values := make(map[string]int64, len(names))
	missing := make([]string, 0, len(names))

	if consistency != ConsistencyStrict {
		for _, name := range names {
			if v, ok := c.versions.get(name); ok {
				values[name] = v
			} else {
				missing = append(missing, name)
			}
		}
	} else {
		missing = names
	}

	if len(missing) > 0 {
		loaded, err := c.store.GetVersions(ctx, missing...)
		if err != nil {
			return versions{}, err
		}
		for _, name := range missing {
			v := loaded[name]
			values[name] = v
			c.versions.set(name, v)
		}
	}

	out := versions{
		Namespace: values[c.namespaceVersionName()],
		Key:       values[c.keyVersionName(key)],
		Tags:      make(map[Tag]int64, len(tags)),
	}
	for _, tag := range tags {
		out.Tags[tag] = values[c.tagVersionName(tag)]
	}
	return out, nil
}

func (c *Client) setLocalVersion(name string, v int64) {
	c.versions.set(name, v)
}
