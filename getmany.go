package cache

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type batchEntry[ID comparable, T any] struct {
	id         ID
	req        request[T]
	storageKey string
	stale      T
	haveStale  bool
	bypass     bool
}

// GetMany resolves a batch with one generation read, one MGET, and one batch
// loader call for misses.
func GetMany[ID comparable, T any](ctx context.Context, c *Client, items []Item[ID], ttl time.Duration, load BatchLoader[ID, T], options ...GetOption) (out BatchResult[ID, T], err error) {
	out.Values = make(map[ID]T, len(items))
	out.Errors = make(map[ID]error)
	out.order = make([]ID, len(items))
	for i := range items {
		out.order[i] = items[i].ID
	}
	if err = c.begin(); err != nil {
		return out, err
	}
	defer c.end()
	if len(items) == 0 {
		return out, ErrNoItems
	}
	if len(items) > c.limits.MaxBatchSize {
		return out, fmt.Errorf("%w: batch too large", ErrInvalidConfig)
	}
	if load == nil {
		return out, ErrNoLoader
	}
	start := time.Now()
	result := ""
	defer func() { observe(c.observer, "get_many", "batch", start, result, err) }()
	entries, err := prepareBatch(ctx, c, items, ttl, load, options)
	if err != nil {
		return out, err
	}
	if c.disabled {
		result = "disabled"
		return loadDisabledBatch(ctx, entries, load)
	}

	remote := make([]*batchEntry[ID, T], 0, len(entries))
	misses := make([]*batchEntry[ID, T], 0, len(entries))
	for i := range entries {
		entry := &entries[i]
		if entry.bypass || entry.req.force {
			misses = append(misses, entry)
			continue
		}
		value, state, hit, cacheErr := readLocal(c, entry.req, entry.storageKey)
		if !hit {
			remote = append(remote, entry)
			continue
		}
		if cacheErr != nil {
			out.Errors[entry.id] = cacheErr
			continue
		}
		if state == recordFresh {
			out.Values[entry.id] = value
			continue
		}
		entry.stale, entry.haveStale = value, true
		if entry.req.staleWhileRevalidate {
			out.Values[entry.id] = value
			refresh(c, ctx, entry.req)
		} else {
			remote = append(remote, entry)
		}
	}

	remoteMisses, remoteErr := readBatchRemote(ctx, c, remote, &out)
	if remoteErr != nil && c.failureMode == FailClosed {
		return out, remoteErr
	}
	misses = append(misses, remoteMisses...)
	if len(misses) == 0 {
		result = "hit"
		err = out.Err()
		return out, err
	}

	loaded, loadErr := loadBatchFlight(ctx, c, misses, load)
	if loadErr != nil {
		for _, entry := range misses {
			if entry.haveStale && entry.req.staleIfErr {
				out.Values[entry.id] = entry.stale
				continue
			}
			out.Errors[entry.id] = loadErr
		}
		result = "load_error"
		err = out.Err()
		return out, err
	}
	for _, entry := range misses {
		value, ok := loaded[entry.id]
		if !ok {
			out.Errors[entry.id] = ErrNotFound
			if !entry.bypass && !entry.req.noStore && entry.req.negativeTTL > 0 {
				_ = storeNegative(ctx, c, entry.req, entry.storageKey)
			}
			continue
		}
		if isNil(value) {
			out.Errors[entry.id] = ErrNoValue
			continue
		}
		if !entry.bypass && !entry.req.noStore {
			if storeErr := storeValue(ctx, c, entry.req, entry.storageKey, value); storeErr != nil {
				observe(c.observer, "l2_set", entry.req.name, time.Now(), "error", storeErr)
			}
		}
		out.Values[entry.id] = value
	}
	result = "load"
	err = out.Err()
	return out, err
}

func prepareBatch[ID comparable, T any](ctx context.Context, c *Client, items []Item[ID], ttl time.Duration, load BatchLoader[ID, T], options []GetOption) ([]batchEntry[ID, T], error) {
	seenIDs := make(map[ID]struct{}, len(items))
	seenKeys := make(map[Key]struct{}, len(items))
	entries := make([]batchEntry[ID, T], 0, len(items))
	for _, item := range items {
		if _, ok := seenIDs[item.ID]; ok {
			return nil, ErrDuplicateID
		}
		if _, ok := seenKeys[item.Key]; ok {
			return nil, ErrDuplicateKey
		}
		seenIDs[item.ID] = struct{}{}
		seenKeys[item.Key] = struct{}{}
		itemOptions := append([]GetOption(nil), options...)
		if len(item.Tags) > 0 {
			itemOptions = append(itemOptions, WithTags(item.Tags...))
		}
		id := item.ID
		req, err := makeRequest(c, item.Key, ttl, func(loadCtx context.Context) (T, error) {
			values, err := load(loadCtx, []ID{id})
			if err != nil {
				var zero T
				return zero, err
			}
			value, ok := values[id]
			if !ok {
				var zero T
				return zero, ErrNotFound
			}
			return value, nil
		}, itemOptions)
		if err != nil {
			return nil, err
		}
		entries = append(entries, batchEntry[ID, T]{id: id, req: req})
	}
	if c.disabled {
		return entries, nil
	}
	routes := make([]*route, len(entries))
	for i := range entries {
		routes[i] = entries[i].req.route
	}
	allVersions, err := c.loadVersionBatch(ctx, routes, entries[0].req.consistency)
	if err != nil {
		if c.failureMode == FailClosed {
			return nil, err
		}
		for i := range entries {
			entries[i].bypass = true
		}
		return entries, nil
	}
	for i := range entries {
		entries[i].storageKey = storageKey(c, entries[i].req.route, allVersions[i])
	}
	return entries, nil
}

func loadDisabledBatch[ID comparable, T any](ctx context.Context, entries []batchEntry[ID, T], load BatchLoader[ID, T]) (BatchResult[ID, T], error) {
	out := BatchResult[ID, T]{Values: make(map[ID]T, len(entries)), Errors: make(map[ID]error)}
	ids := batchIDs(entries)
	out.order = append(out.order, ids...)
	values, err := load(ctx, ids)
	if err != nil {
		for _, id := range ids {
			out.Errors[id] = err
		}
		return out, out.Err()
	}
	for _, id := range ids {
		value, ok := values[id]
		if !ok {
			out.Errors[id] = ErrNotFound
		} else if isNil(value) {
			out.Errors[id] = ErrNoValue
		} else {
			out.Values[id] = value
		}
	}
	return out, out.Err()
}

func readBatchRemote[ID comparable, T any](ctx context.Context, c *Client, entries []*batchEntry[ID, T], out *BatchResult[ID, T]) ([]*batchEntry[ID, T], error) {
	if len(entries) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(entries))
	byStorage := make(map[string]*batchEntry[ID, T], len(entries))
	misses := make([]*batchEntry[ID, T], 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.storageKey)
		byStorage[entry.storageKey] = entry
	}
	data, err := c.store.MGet(ctx, keys...)
	if err != nil {
		return entries, err
	}
	now := time.Now()
	for _, physicalKey := range keys {
		entry := byStorage[physicalKey]
		body, ok := data[physicalKey]
		if !ok {
			observe(c.observer, "l2", entry.req.name, time.Now(), "miss", nil)
			misses = append(misses, entry)
			continue
		}
		value, state, localTTL, decodeErr := decodeValue(entry.req, body, now)
		if errors.Is(decodeErr, ErrNotFound) {
			c.local.Set(physicalKey, body, int64(len(body)), localTTL)
			observe(c.observer, "l2", entry.req.name, time.Now(), "negative", nil)
			out.Errors[entry.id] = ErrNotFound
			continue
		}
		if decodeErr != nil || state == recordExpired {
			_ = c.store.Delete(ctx, physicalKey)
			misses = append(misses, entry)
			continue
		}
		observe(c.observer, "l2", entry.req.name, time.Now(), "hit", nil)
		c.local.Set(physicalKey, body, int64(len(body)), localTTL)
		if state == recordFresh {
			out.Values[entry.id] = value
			continue
		}
		entry.stale, entry.haveStale = value, true
		if entry.req.staleWhileRevalidate {
			out.Values[entry.id] = value
			refresh(c, ctx, entry.req)
		} else {
			misses = append(misses, entry)
		}
	}
	return misses, nil
}

func loadBatchFlight[ID comparable, T any](ctx context.Context, c *Client, entries []*batchEntry[ID, T], load BatchLoader[ID, T]) (map[ID]T, error) {
	ids := batchIDsFromPointers(entries)
	parts := make([]string, len(entries))
	for i, entry := range entries {
		parts[i] = entry.storageKey
		if parts[i] == "" {
			parts[i] = string(entry.req.key) + "|" + entry.req.typeID
		}
	}
	sort.Strings(parts)
	flightKey := "batch:" + hashHex([]byte(strings.Join(parts, "|")))
	value, err, shared := c.flights.do(ctx, flightKey,
		func() { c.workers.Add(1) },
		func() { c.workers.Done() },
		func() (any, error) {
			loadCtx, cancel := c.loaderContext(ctx, c.limits.LoadTimeout)
			defer cancel()
			start := time.Now()
			values, err := load(loadCtx, ids)
			result := "success"
			if err != nil {
				result = "error"
			}
			observe(c.observer, "load_many", "batch", start, result, err)
			return values, err
		},
	)
	if shared {
		observe(c.observer, "singleflight", "batch", time.Now(), "shared", nil)
	}
	if err != nil {
		return nil, err
	}
	values, ok := value.(map[ID]T)
	if !ok {
		return nil, ErrTypeMismatch
	}
	return values, nil
}

func batchIDs[ID comparable, T any](entries []batchEntry[ID, T]) []ID {
	ids := make([]ID, len(entries))
	for i := range entries {
		ids[i] = entries[i].id
	}
	return ids
}

func batchIDsFromPointers[ID comparable, T any](entries []*batchEntry[ID, T]) []ID {
	ids := make([]ID, len(entries))
	for i, entry := range entries {
		ids[i] = entry.id
	}
	return ids
}
