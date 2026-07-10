package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

type versions struct {
	namespace int64
	key       int64
	tags      []int64
}

type cachedVersion struct {
	name      string
	value     int64
	expiresAt time.Time
}

type versionCache struct {
	ttl    time.Duration
	shards [localShardCount]versionShard
}

type versionShard struct {
	mu      sync.Mutex
	max     int
	entries map[string]*list.Element
	lru     list.List
}

func newVersionCache(ttl time.Duration, maxEntries int) *versionCache {
	cache := &versionCache{ttl: ttl}
	perShard := maxEntries / localShardCount
	if perShard < 1 {
		perShard = 1
	}
	for i := range cache.shards {
		cache.shards[i].max = perShard
		cache.shards[i].entries = make(map[string]*list.Element)
	}
	return cache
}

func (vc *versionCache) get(name string, now time.Time) (int64, bool) {
	shard := &vc.shards[localHash(name)&(localShardCount-1)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	element := shard.entries[name]
	if element == nil {
		return 0, false
	}
	version := element.Value.(*cachedVersion)
	if !now.Before(version.expiresAt) {
		delete(shard.entries, name)
		shard.lru.Remove(element)
		return 0, false
	}
	shard.lru.MoveToFront(element)
	return version.value, true
}

func (vc *versionCache) setMax(name string, value int64, now time.Time) int64 {
	shard := &vc.shards[localHash(name)&(localShardCount-1)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if element := shard.entries[name]; element != nil {
		current := element.Value.(*cachedVersion)
		if current.value > value {
			value = current.value
		}
		current.value = value
		current.expiresAt = now.Add(vc.ttl)
		shard.lru.MoveToFront(element)
		return value
	}
	version := &cachedVersion{name: name, value: value, expiresAt: now.Add(vc.ttl)}
	shard.entries[name] = shard.lru.PushFront(version)
	for len(shard.entries) > shard.max {
		last := shard.lru.Back()
		delete(shard.entries, last.Value.(*cachedVersion).name)
		shard.lru.Remove(last)
	}
	return value
}

func (vc *versionCache) clear() {
	for i := range vc.shards {
		shard := &vc.shards[i]
		shard.mu.Lock()
		shard.entries = make(map[string]*list.Element)
		shard.lru.Init()
		shard.mu.Unlock()
	}
}

func (c *Client) namespaceVersionName() string { return c.namespaceVersion }
func (c *Client) keyVersionName(key Key) string {
	return c.namespace + ":version:key:" + hashHex([]byte(key))
}
func (c *Client) tagVersionName(tag Tag) string {
	return c.namespace + ":version:tag:" + hashHex([]byte(tag))
}

func (c *Client) loadVersions(ctx context.Context, route *route, consistency Consistency) (versions, error) {
	now := time.Now()
	out := versions{tags: make([]int64, len(route.tagVersions))}
	var missing []string
	if consistency == Fast {
		var ok bool
		out.namespace, ok = c.versions.get(c.namespaceVersion, now)
		if !ok {
			missing = append(missing, c.namespaceVersion)
		}
		out.key, ok = c.versions.get(route.keyVersion, now)
		if !ok {
			missing = append(missing, route.keyVersion)
		}
		for i, name := range route.tagVersions {
			out.tags[i], ok = c.versions.get(name, now)
			if !ok {
				missing = append(missing, name)
			}
		}
	} else {
		missing = make([]string, 0, len(route.tagVersions)+2)
		missing = append(missing, c.namespaceVersion, route.keyVersion)
		missing = append(missing, route.tagVersions...)
	}
	if len(missing) == 0 {
		return out, nil
	}
	loaded, err := c.store.GetVersions(ctx, missing...)
	if err != nil {
		return versions{}, err
	}
	now = time.Now()
	for _, name := range missing {
		loaded[name] = c.versions.setMax(name, loaded[name], now)
	}
	if value, ok := loaded[c.namespaceVersion]; ok {
		out.namespace = value
	}
	if value, ok := loaded[route.keyVersion]; ok {
		out.key = value
	}
	for i, name := range route.tagVersions {
		if value, ok := loaded[name]; ok {
			out.tags[i] = value
		}
	}
	return out, nil
}

type missingVersions struct {
	namespace bool
	key       bool
	tags      []bool
}

func (c *Client) loadVersionBatch(ctx context.Context, routes []*route, consistency Consistency) ([]versions, error) {
	now := time.Now()
	out := make([]versions, len(routes))
	missingByRoute := make([]missingVersions, len(routes))
	missingNames := make([]string, 0)
	seen := make(map[string]struct{})
	addMissing := func(name string) {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			missingNames = append(missingNames, name)
		}
	}
	for i, route := range routes {
		out[i].tags = make([]int64, len(route.tagVersions))
		missingByRoute[i].tags = make([]bool, len(route.tagVersions))
		if consistency == Fast {
			var ok bool
			out[i].namespace, ok = c.versions.get(c.namespaceVersion, now)
			if !ok {
				missingByRoute[i].namespace = true
				addMissing(c.namespaceVersion)
			}
			out[i].key, ok = c.versions.get(route.keyVersion, now)
			if !ok {
				missingByRoute[i].key = true
				addMissing(route.keyVersion)
			}
			for j, name := range route.tagVersions {
				out[i].tags[j], ok = c.versions.get(name, now)
				if !ok {
					missingByRoute[i].tags[j] = true
					addMissing(name)
				}
			}
		} else {
			missingByRoute[i].namespace = true
			missingByRoute[i].key = true
			for j := range route.tagVersions {
				missingByRoute[i].tags[j] = true
			}
			addMissing(c.namespaceVersion)
			addMissing(route.keyVersion)
			for _, name := range route.tagVersions {
				addMissing(name)
			}
		}
	}
	if len(missingNames) == 0 {
		return out, nil
	}
	loaded, err := c.store.GetVersions(ctx, missingNames...)
	if err != nil {
		return nil, err
	}
	now = time.Now()
	for _, name := range missingNames {
		loaded[name] = c.versions.setMax(name, loaded[name], now)
	}
	for i, route := range routes {
		missing := missingByRoute[i]
		if missing.namespace {
			out[i].namespace = loaded[c.namespaceVersion]
		}
		if missing.key {
			out[i].key = loaded[route.keyVersion]
		}
		for j, name := range route.tagVersions {
			if missing.tags[j] {
				out[i].tags[j] = loaded[name]
			}
		}
	}
	return out, nil
}
