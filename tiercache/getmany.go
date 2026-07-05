package tiercache

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type batchEntry[T any] struct {
	req        GetRequest[T]
	versions   versions
	storageKey string
	staleValue T
	haveStale  bool
	noStore    bool
}

// GetMany returns cached values for req.Items, loading and storing misses in
// one batch.
func GetMany[T any](ctx context.Context, c *Client, req GetManyRequest[T]) (BatchResult[T], error) {
	start := time.Now()
	name := req.Name
	if name == "" {
		name = "batch"
	}
	var result string
	var err error
	defer func() {
		observe(cObserver(c), "get_many", name, start, result, err)
	}()

	out := BatchResult[T]{
		Values: make(map[Key]T, len(req.Items)),
		Errors: make(map[Key]error),
	}
	if c == nil {
		err = ErrNoClient
		return out, err
	}
	var span spanHandle
	ctx, span = startGetManySpan(ctx, c, name, len(req.Items))
	defer func() {
		endGetSpan(span, result, err)
	}()
	if c.isClosed() {
		err = ErrClosed
		return out, err
	}
	if e := ctx.Err(); e != nil {
		err = e
		return out, err
	}
	if len(req.Items) == 0 {
		err = ErrNoItems
		return out, err
	}
	if req.Load == nil {
		err = ErrNoLoader
		return out, err
	}
	if c.disabled {
		keys, prepareErr := disabledBatchKeys(req.Items)
		if prepareErr != nil {
			err = prepareErr
			return out, err
		}
		loaded, loadErr := callBatchLoader(ctx, c, name, req.Load, keys)
		if loadErr != nil {
			for _, key := range keys {
				out.Errors[key] = loadErr
			}
			result = "disabled_error"
			err = out.Err()
			return out, err
		}
		for _, key := range keys {
			value, ok := loaded[key]
			if !ok {
				out.Errors[key] = ErrNotFound
				continue
			}
			if isNil(value) {
				out.Errors[key] = ErrNoValue
				continue
			}
			out.Values[key] = value
		}
		result = "disabled"
		err = out.Err()
		return out, err
	}

	entries, err := prepareBatchEntries(ctx, c, req)
	if err != nil {
		return out, err
	}

	misses := make([]*batchEntry[T], 0, len(entries))
	remote := make([]*batchEntry[T], 0, len(entries))
	for i := range entries {
		entry := &entries[i]
		if entry.req.Policy.ForceRefresh {
			misses = append(misses, entry)
			continue
		}
		value, ok, stale, cacheErr := getLocal(ctx, c, entry.req, entry.storageKey, entry.versions)
		if ok {
			if cacheErr != nil {
				out.Errors[entry.req.Key] = cacheErr
				continue
			}
			if stale {
				entry.staleValue = value
				entry.haveStale = true
				if entry.req.Policy.StaleWhileRevalidate {
					out.Values[entry.req.Key] = value
					observe(c.observer, "stale", entry.req.Name, time.Now(), "return", nil)
					refresh(c, entry.req, entry.versions)
					continue
				}
				misses = append(misses, entry)
				continue
			}
			out.Values[entry.req.Key] = value
			continue
		}
		remote = append(remote, entry)
	}

	misses = append(misses, getManyRemote(ctx, c, remote, &out)...)
	if len(misses) == 0 {
		result = "hit"
		err = out.Err()
		return out, err
	}

	loadedAny, loadErr, shared := c.sf.Do(batchFlightKey(misses), func() (any, error) {
		return callBatchLoader(ctx, c, name, req.Load, batchKeys(misses))
	})
	if loadErr != nil {
		for _, entry := range misses {
			if entry.haveStale && entry.req.Policy.StaleIfError {
				out.Values[entry.req.Key] = entry.staleValue
				observe(c.observer, "stale", entry.req.Name, time.Now(), "if_error", nil)
				continue
			}
			out.Errors[entry.req.Key] = loadErr
		}
		result = "load_error"
		err = out.Err()
		return out, err
	}
	loaded := loadedAny.(map[Key]T)
	if shared {
		observe(c.observer, "singleflight", name, time.Now(), "shared", nil)
	}

	for _, entry := range misses {
		value, ok := loaded[entry.req.Key]
		if !ok {
			out.Errors[entry.req.Key] = ErrNotFound
			if !entry.req.Policy.NoStore && !entry.noStore {
				_ = storeNegative(ctx, c, entry.req, entry.storageKey, entry.versions)
			}
			continue
		}
		if isNil(value) {
			out.Errors[entry.req.Key] = ErrNoValue
			continue
		}
		if !entry.req.Policy.NoStore && !entry.noStore {
			if e := storeValue(ctx, c, entry.req, entry.storageKey, entry.versions, value); e != nil {
				observe(c.observer, "l2_set", entry.req.Name, time.Now(), "error", e)
			}
		}
		out.Values[entry.req.Key] = value
	}

	result = "load"
	err = out.Err()
	return out, err
}

func prepareBatchEntries[T any](ctx context.Context, c *Client, req GetManyRequest[T]) ([]batchEntry[T], error) {
	seen := make(map[Key]struct{}, len(req.Items))
	entries := make([]batchEntry[T], 0, len(req.Items))
	for _, item := range req.Items {
		if err := validateKey(item.Key); err != nil {
			return nil, err
		}
		if _, ok := seen[item.Key]; ok {
			return nil, ErrDuplicateKey
		}
		seen[item.Key] = struct{}{}

		item.Tags = canonicalTags(item.Tags)
		if err := validateTags(item.Tags); err != nil {
			return nil, err
		}
		itemName := item.Name
		if itemName == "" {
			itemName = keyName(item.Key)
		}
		getReq := GetRequest[T]{
			Key:         item.Key,
			Tags:        item.Tags,
			Name:        itemName,
			TTL:         req.TTL,
			Stale:       req.Stale,
			NegativeTTL: req.NegativeTTL,
			Codec:       req.Codec,
			Policy:      req.Policy,
		}
		getReq = applyDefaults(c, getReq)
		if getReq.TTL <= 0 || getReq.TTL > c.limits.MaxTTL {
			return nil, ErrInvalidTTL
		}
		if getReq.Stale < 0 || getReq.Stale > c.limits.MaxStale {
			return nil, ErrInvalidTTL
		}
		if getReq.Codec == nil {
			getReq.Codec = c.codec
		}
		v, err := c.loadVersions(ctx, getReq.Key, getReq.Tags, getReq.Policy.Consistency)
		if err != nil {
			if !c.failOpen {
				return nil, err
			}
			entries = append(entries, batchEntry[T]{
				req:     getReq,
				noStore: true,
			})
			continue
		}
		entries = append(entries, batchEntry[T]{
			req:        getReq,
			versions:   v,
			storageKey: c.storageKey(getReq.Key, getReq.Tags, v, getReq.Codec),
		})
	}
	return entries, nil
}

func disabledBatchKeys(items []BatchItem) ([]Key, error) {
	seen := make(map[Key]struct{}, len(items))
	keys := make([]Key, len(items))
	for i, item := range items {
		if err := validateKey(item.Key); err != nil {
			return nil, err
		}
		if _, ok := seen[item.Key]; ok {
			return nil, ErrDuplicateKey
		}
		seen[item.Key] = struct{}{}
		keys[i] = item.Key
	}
	return keys, nil
}

func getManyRemote[T any](ctx context.Context, c *Client, entries []*batchEntry[T], out *BatchResult[T]) []*batchEntry[T] {
	if len(entries) == 0 {
		return nil
	}
	storageKeys := make([]string, 0, len(entries))
	byStorageKey := make(map[string]*batchEntry[T], len(entries))
	remoteEntries := make([]*batchEntry[T], 0, len(entries))
	misses := make([]*batchEntry[T], 0, len(entries))
	for _, entry := range entries {
		if entry.noStore {
			misses = append(misses, entry)
			continue
		}
		storageKeys = append(storageKeys, entry.storageKey)
		byStorageKey[entry.storageKey] = entry
		remoteEntries = append(remoteEntries, entry)
	}
	if len(storageKeys) == 0 {
		return misses
	}
	data, err := c.store.MGet(ctx, storageKeys...)
	if err != nil {
		observe(c.observer, "l2_mget", "", time.Now(), "error", err)
		return append(misses, remoteEntries...)
	}
	for _, storageKey := range storageKeys {
		entry := byStorageKey[storageKey]
		body, ok := data[storageKey]
		if !ok {
			misses = append(misses, entry)
			continue
		}
		value, stale, negative, err := decodeCached(ctx, c, entry.req, storageKey, entry.versions, body)
		if err != nil {
			if negative && errors.Is(err, ErrNotFound) {
				ttl := cachedTTL(body)
				c.local.Set(storageKey, body, int64(len(body)), ttl)
				c.index.add(entry.req.Key, entry.req.Tags, storageKey, time.Now().Add(ttl))
				out.Errors[entry.req.Key] = ErrNotFound
				continue
			}
			if !errors.Is(err, errCachedExpired) {
				_ = c.store.Delete(ctx, storageKey)
			}
			misses = append(misses, entry)
			continue
		}
		ttl := cachedTTL(body)
		c.local.Set(storageKey, body, int64(len(body)), ttl)
		c.index.add(entry.req.Key, entry.req.Tags, storageKey, time.Now().Add(ttl))
		if stale {
			entry.staleValue = value
			entry.haveStale = true
			if entry.req.Policy.StaleWhileRevalidate {
				out.Values[entry.req.Key] = value
				observe(c.observer, "stale", entry.req.Name, time.Now(), "return", nil)
				refresh(c, entry.req, entry.versions)
				continue
			}
			misses = append(misses, entry)
			continue
		}
		out.Values[entry.req.Key] = value
	}
	return misses
}

func batchKeys[T any](entries []*batchEntry[T]) []Key {
	keys := make([]Key, len(entries))
	for i, entry := range entries {
		keys[i] = entry.req.Key
	}
	return keys
}

func batchFlightKey[T any](entries []*batchEntry[T]) string {
	keys := make([]string, len(entries))
	for i, entry := range entries {
		if entry.storageKey != "" {
			keys[i] = entry.storageKey
			continue
		}
		keys[i] = string(entry.req.Key)
	}
	sort.Strings(keys)
	return "many:" + hashHex([]byte(strings.Join(keys, "|")))
}

func callBatchLoader[T any](ctx context.Context, c *Client, name string, load BatchLoader[T], keys []Key) (map[Key]T, error) {
	start := time.Now()
	var span spanHandle
	ctx, span = startCacheSpan(ctx, c, "tiercache.load", attribute.String("cache.name", name), attribute.Int("cache.item_count", len(keys)))
	values, err := load(ctx, keys)
	result := "success"
	if err != nil {
		result = "error"
	}
	endGetSpan(span, result, err)
	observe(c.observer, "load_many", name, start, result, err)
	return values, err
}
