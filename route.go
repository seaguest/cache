package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

type routeKey struct {
	key       Key
	typeID    string
	schema    string
	codec     string
	firstTag  Tag
	tags      string
	ttl       time.Duration
	stale     time.Duration
	negative  time.Duration
	staleMode uint8
}

type route struct {
	name        string
	keyVersion  string
	tagVersions []string
	digest      [32]byte
}

type routeCache struct {
	shards [localShardCount]routeShard
}

type routeShard struct {
	mu      sync.Mutex
	max     int
	entries map[routeKey]*list.Element
	lru     list.List
}

type routeEntry struct {
	key   routeKey
	value *route
}

func newRouteCache(maxEntries int) *routeCache {
	cache := &routeCache{}
	perShard := maxEntries / localShardCount
	if perShard < 1 {
		perShard = 1
	}
	for i := range cache.shards {
		cache.shards[i].max = perShard
		cache.shards[i].entries = make(map[routeKey]*list.Element)
	}
	return cache
}

func (c *routeCache) get(key routeKey) (*route, bool) {
	shard := &c.shards[localHash(string(key.key))&(localShardCount-1)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	element := shard.entries[key]
	if element == nil {
		return nil, false
	}
	shard.lru.MoveToFront(element)
	return element.Value.(*routeEntry).value, true
}

func (c *routeCache) set(key routeKey, value *route) *route {
	shard := &c.shards[localHash(string(key.key))&(localShardCount-1)]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if element := shard.entries[key]; element != nil {
		shard.lru.MoveToFront(element)
		return element.Value.(*routeEntry).value
	}
	shard.entries[key] = shard.lru.PushFront(&routeEntry{key: key, value: value})
	for len(shard.entries) > shard.max {
		last := shard.lru.Back()
		delete(shard.entries, last.Value.(*routeEntry).key)
		shard.lru.Remove(last)
	}
	return value
}

var typeNames sync.Map

func cachedTypeID[T any]() string {
	t := reflect.TypeFor[T]()
	if name, ok := typeNames.Load(t); ok {
		return name.(string)
	}
	name := stableTypeName(t)
	actual, _ := typeNames.LoadOrStore(t, name)
	return actual.(string)
}

func (c *Client) routeFor(key Key, valueType, schema string, codec Codec, tags []Tag, ttl, stale, negative time.Duration, staleMode uint8) *route {
	tagIdentity := ""
	firstTag := Tag("")
	if len(tags) == 1 {
		firstTag = tags[0]
	} else if len(tags) > 1 {
		var b strings.Builder
		for _, tag := range tags {
			b.WriteString(strconv.Itoa(len(tag)))
			b.WriteByte(':')
			b.WriteString(string(tag))
		}
		tagIdentity = b.String()
	}
	rk := routeKey{
		key: key, typeID: valueType, schema: schema, codec: codec.Name(),
		firstTag: firstTag, tags: tagIdentity, ttl: ttl, stale: stale,
		negative: negative, staleMode: staleMode,
	}
	if cached, ok := c.routes.get(rk); ok {
		return cached
	}
	var material bytes.Buffer
	writeRoutePart(&material, c.namespace)
	writeRoutePart(&material, string(key))
	writeRoutePart(&material, valueType)
	writeRoutePart(&material, schema)
	writeRoutePart(&material, codec.Name())
	writeRouteInt(&material, int64(ttl))
	writeRouteInt(&material, int64(stale))
	writeRouteInt(&material, int64(negative))
	writeRouteInt(&material, int64(staleMode))
	for _, tag := range tags {
		writeRoutePart(&material, string(tag))
	}
	created := &route{
		name:        keyName(key),
		keyVersion:  c.keyVersionName(key),
		tagVersions: make([]string, len(tags)),
		digest:      sha256.Sum256(material.Bytes()),
	}
	for i, tag := range tags {
		created.tagVersions[i] = c.tagVersionName(tag)
	}
	return c.routes.set(rk, created)
}

func writeRoutePart(dst *bytes.Buffer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	dst.Write(size[:])
	dst.WriteString(value)
}

func writeRouteInt(dst *bytes.Buffer, value int64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(value))
	dst.Write(data[:])
}
