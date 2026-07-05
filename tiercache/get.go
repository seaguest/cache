package tiercache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

var errCachedExpired = errors.New("tiercache: cached record expired")

// Get returns the cached value for req.Key, loading and storing it on miss.
func Get[T any](ctx context.Context, c *Client, req GetRequest[T]) (T, error) {
	var zero T
	start := time.Now()
	name := req.Name
	if name == "" {
		name = keyName(req.Key)
	}
	req.Name = name
	var result string
	var err error
	defer func() {
		observe(cObserver(c), "get", name, start, result, err)
	}()

	if c == nil {
		err = ErrNoClient
		return zero, err
	}
	var span spanHandle
	ctx, span = startGetSpan(ctx, c, name, req.Key, req.Tags)
	defer func() {
		endGetSpan(span, result, err)
	}()
	if c.isClosed() {
		err = ErrClosed
		return zero, err
	}
	if e := ctx.Err(); e != nil {
		err = e
		return zero, err
	}
	if err = validateKey(req.Key); err != nil {
		return zero, err
	}
	req.Tags = canonicalTags(req.Tags)
	if err = validateTags(req.Tags); err != nil {
		return zero, err
	}
	if req.Load == nil {
		err = ErrNoLoader
		return zero, err
	}

	req = applyDefaults(c, req)
	if req.TTL <= 0 || req.TTL > c.limits.MaxTTL {
		err = ErrInvalidTTL
		return zero, err
	}
	if req.Stale < 0 || req.Stale > c.limits.MaxStale {
		err = ErrInvalidTTL
		return zero, err
	}
	if req.Policy.Consistency == ConsistencyDefault {
		req.Policy.Consistency = c.defaults.Consistency
	}
	if req.Codec == nil {
		req.Codec = c.codec
	}
	if c.disabled {
		value, loadErr := callLoader(ctx, c, req)
		if loadErr != nil {
			err = loadErr
			result = "disabled_error"
			return zero, err
		}
		if isNil(value) {
			err = ErrNoValue
			result = "disabled_error"
			return zero, err
		}
		result = "disabled"
		return value, nil
	}

	v, genErr := c.loadVersions(ctx, req.Key, req.Tags, req.Policy.Consistency)
	if genErr != nil {
		if c.failOpen {
			result = "version_error_load"
			value, loadErr := req.Load(ctx)
			if loadErr != nil {
				err = loadErr
				return zero, err
			}
			return value, nil
		}
		err = genErr
		return zero, err
	}
	storageKey := c.storageKey(req.Key, req.Tags, v, req.Codec)

	var staleValue T
	var haveStale bool

	if !req.Policy.ForceRefresh {
		localStart := time.Now()
		localCtx, localSpan := startCacheSpan(ctx, c, "tiercache.l1")
		value, ok, stale, cacheErr := getLocal(ctx, c, req, storageKey, v)
		if ok {
			endGetSpan(localSpan, "hit", cacheErr)
			observe(c.observer, "l1", req.Name, localStart, "hit", nil)
			if cacheErr != nil {
				err = cacheErr
				result = "l1_negative"
				return zero, err
			}
			result = "l1_hit"
			if stale {
				staleValue = value
				haveStale = true
				if req.Policy.StaleWhileRevalidate {
					observe(c.observer, "stale", req.Name, time.Now(), "return", nil)
					refresh(c, req, v)
					return value, nil
				}
			} else {
				return value, nil
			}
		} else {
			endGetSpan(localSpan, "miss", nil)
			observe(c.observer, "l1", req.Name, localStart, "miss", nil)
		}
		remoteStart := time.Now()
		remoteCtx, remoteSpan := startCacheSpan(localCtx, c, "tiercache.l2")
		value, ok, stale, cacheErr = getRemote(remoteCtx, c, req, storageKey, v)
		if ok {
			endGetSpan(remoteSpan, "hit", cacheErr)
			observe(c.observer, "l2", req.Name, remoteStart, "hit", nil)
			if cacheErr != nil {
				err = cacheErr
				result = "l2_negative"
				return zero, err
			}
			result = "l2_hit"
			if stale {
				staleValue = value
				haveStale = true
				if req.Policy.StaleWhileRevalidate {
					observe(c.observer, "stale", req.Name, time.Now(), "return", nil)
					refresh(c, req, v)
					return value, nil
				}
			} else {
				return value, nil
			}
		} else {
			endGetSpan(remoteSpan, "miss", nil)
			observe(c.observer, "l2", req.Name, remoteStart, "miss", nil)
		}
	}

	value, loadErr := loadAndStore(ctx, c, req, storageKey, v)
	if loadErr != nil {
		if haveStale && req.Policy.StaleIfError {
			observe(c.observer, "stale", req.Name, time.Now(), "if_error", nil)
			result = "stale_if_error"
			return staleValue, nil
		}
		err = loadErr
		result = "load_error"
		return zero, err
	}
	result = "load"
	return value, nil
}

func cObserver(c *Client) Observer {
	if c == nil {
		return nil
	}
	return c.observer
}

func applyDefaults[T any](c *Client, req GetRequest[T]) GetRequest[T] {
	if req.TTL == 0 {
		req.TTL = c.defaults.TTL
	}
	if req.Stale == 0 {
		req.Stale = c.defaults.Stale
	}
	if req.NegativeTTL == 0 {
		req.NegativeTTL = c.defaults.NegativeTTL
	}
	if req.Policy == (Policy{}) {
		req.Policy = c.defaults.Policy
	}
	if req.Policy.Consistency == ConsistencyDefault {
		req.Policy.Consistency = c.defaults.Consistency
	}
	return req
}

func getLocal[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) (T, bool, bool, error) {
	var zero T
	data, ok := c.local.Get(storageKey)
	if !ok {
		return zero, false, false, nil
	}
	value, stale, negative, err := decodeCached(ctx, c, req, storageKey, v, data)
	if err != nil {
		if negative && errors.Is(err, ErrNotFound) {
			return zero, true, stale, ErrNotFound
		}
		if errors.Is(err, errCachedExpired) {
			c.local.Delete(storageKey)
			return zero, false, false, nil
		}
		c.local.Delete(storageKey)
		return zero, false, false, nil
	}
	return value, true, stale, nil
}

func getRemote[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) (T, bool, bool, error) {
	var zero T
	data, err := c.store.Get(ctx, storageKey)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return zero, false, false, nil
		}
		observe(c.observer, "l2_get", req.Name, time.Now(), "error", err)
		return zero, false, false, nil
	}
	value, stale, negative, err := decodeCached(ctx, c, req, storageKey, v, data)
	if err != nil {
		if negative && errors.Is(err, ErrNotFound) {
			ttl := cachedTTL(data)
			c.local.Set(storageKey, data, int64(len(data)), ttl)
			c.index.add(req.Key, req.Tags, storageKey, time.Now().Add(ttl))
			return zero, true, stale, ErrNotFound
		}
		if errors.Is(err, errCachedExpired) {
			return zero, false, false, nil
		}
		_ = c.store.Delete(ctx, storageKey)
		return zero, false, false, nil
	}
	ttl := cachedTTL(data)
	c.local.Set(storageKey, data, int64(len(data)), ttl)
	c.index.add(req.Key, req.Tags, storageKey, time.Now().Add(ttl))
	return value, true, stale, nil
}

func decodeCached[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions, data []byte) (T, bool, bool, error) {
	var zero T
	r, err := decodeRecord(data)
	if err != nil {
		return zero, false, false, err
	}
	if r.StorageHash != fingerprint(storageKey) {
		return zero, false, false, ErrInvalidKey
	}
	if r.NamespaceVer != v.Namespace || r.KeyHash != hashHex([]byte(req.Key)) || r.KeyVersion != v.Key {
		return zero, false, false, ErrInvalidKey
	}
	for _, tag := range req.Tags {
		tagHash := hashHex([]byte(tag))
		if r.TagVersions[tagHash] != v.Tags[tag] {
			return zero, false, false, ErrInvalidTag
		}
	}
	state := r.state(time.Now())
	if state == recordExpired {
		return zero, false, false, errCachedExpired
	}
	if r.Negative {
		return zero, state == recordStale, true, ErrNotFound
	}
	var value T
	if err := req.Codec.Unmarshal(r.Payload, &value); err != nil {
		observe(c.observer, "decode", req.Name, time.Now(), "error", err)
		return zero, false, false, err
	}
	if isNil(value) {
		return zero, false, false, ErrNoValue
	}
	return value, state == recordStale, false, nil
}

func cachedTTL(data []byte) time.Duration {
	r, err := decodeRecord(data)
	if err != nil {
		return 0
	}
	return r.ttl(time.Now())
}

func loadAndStore[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) (T, error) {
	var zero T
	it, err, shared := c.sf.Do(storageKey, func() (any, error) {
		if !req.Policy.ForceRefresh {
			if value, ok, _, cacheErr := getRemote(ctx, c, req, storageKey, v); ok {
				if cacheErr != nil {
					return zero, cacheErr
				}
				return value, nil
			}
		}
		value, err := loadWithStampedeProtection(ctx, c, req, storageKey, v)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				if !req.Policy.NoStore {
					_ = storeNegative(ctx, c, req, storageKey, v)
				}
				return zero, ErrNotFound
			}
			return zero, err
		}
		if isNil(value) {
			return zero, ErrNoValue
		}
		if !req.Policy.NoStore {
			if err := storeValue(ctx, c, req, storageKey, v, value); err != nil {
				observe(c.observer, "l2_set", req.Name, time.Now(), "error", err)
			}
		}
		return value, nil
	})
	if err != nil {
		return zero, err
	}
	if shared {
		observe(c.observer, "singleflight", req.Name, time.Now(), "shared", nil)
	}
	return it.(T), nil
}

func loadWithStampedeProtection[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) (T, error) {
	var zero T
	if !c.stampede.Distributed {
		return callLoader(ctx, c, req)
	}
	lockStore, ok := c.store.(LockStore)
	if !ok {
		return zero, ErrNoLockStore
	}
	lockName := c.namespace + ":lock:" + hashHex([]byte(storageKey))
	token, locked, err := lockStore.TryLock(ctx, lockName, c.stampede.LockTTL)
	if err != nil {
		observe(c.observer, "lock", req.Name, time.Now(), "error", err)
		return callLoader(ctx, c, req)
	}
	if locked {
		defer func() {
			if err := lockStore.Unlock(context.Background(), lockName, token); err != nil {
				observe(c.observer, "unlock", req.Name, time.Now(), "error", err)
			}
		}()
		return loadAfterDistributedLock(ctx, c, req, storageKey, v)
	}

	waitCtx, cancel := context.WithTimeout(ctx, c.stampede.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(c.stampede.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			token, locked, err = lockStore.TryLock(ctx, lockName, c.stampede.LockTTL)
			if err != nil || !locked {
				if err != nil {
					observe(c.observer, "lock", req.Name, time.Now(), "error", err)
				}
				return callLoader(ctx, c, req)
			}
			defer func() {
				if err := lockStore.Unlock(context.Background(), lockName, token); err != nil {
					observe(c.observer, "unlock", req.Name, time.Now(), "error", err)
				}
			}()
			return loadAfterDistributedLock(ctx, c, req, storageKey, v)
		case <-ticker.C:
			if value, ok, _, cacheErr := getRemote(waitCtx, c, req, storageKey, v); ok {
				return value, cacheErr
			}
		}
	}
}

func loadAfterDistributedLock[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) (T, error) {
	if !req.Policy.ForceRefresh {
		if value, ok, _, cacheErr := getRemote(ctx, c, req, storageKey, v); ok {
			return value, cacheErr
		}
	}
	return callLoader(ctx, c, req)
}

func callLoader[T any](ctx context.Context, c *Client, req GetRequest[T]) (T, error) {
	start := time.Now()
	var span spanHandle
	ctx, span = startCacheSpan(ctx, c, "tiercache.load", attribute.String("cache.name", req.Name))
	value, err := req.Load(ctx)
	result := "success"
	if err != nil {
		result = "error"
		if errors.Is(err, ErrNotFound) {
			result = "not_found"
		}
	}
	endGetSpan(span, result, err)
	observe(c.observer, "load", req.Name, start, result, err)
	return value, err
}

func lockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func storeValue[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions, value T) error {
	payload, err := req.Codec.Marshal(value)
	if err != nil {
		observe(c.observer, "encode", req.Name, time.Now(), "error", err)
		return err
	}
	now := time.Now()
	hardTTL := req.TTL + req.Stale
	rec := record{
		Version:      1,
		Codec:        req.Codec.Name(),
		Payload:      payload,
		CreatedAt:    now.UnixMilli(),
		SoftExpireAt: now.Add(req.TTL).UnixMilli(),
		HardExpireAt: now.Add(hardTTL).UnixMilli(),
		KeyHash:      hashHex([]byte(req.Key)),
		TagHashes:    tagHashes(req.Tags),
		NamespaceVer: v.Namespace,
		KeyVersion:   v.Key,
		TagVersions:  tagVersionMap(req.Tags, v),
		StorageHash:  fingerprint(storageKey),
	}
	data, err := encodeRecord(rec)
	if err != nil {
		observe(c.observer, "encode", req.Name, time.Now(), "error", err)
		return err
	}
	if err := c.store.Set(ctx, storageKey, data, ttlWithJitter(storageKey, hardTTL)); err != nil {
		return err
	}
	localTTL := rec.ttl(now)
	c.local.Set(storageKey, data, int64(len(data)), localTTL)
	c.index.add(req.Key, req.Tags, storageKey, now.Add(localTTL))
	return nil
}

func storeNegative[T any](ctx context.Context, c *Client, req GetRequest[T], storageKey string, v versions) error {
	now := time.Now()
	ttl := req.NegativeTTL
	rec := record{
		Version:      1,
		Codec:        req.Codec.Name(),
		CreatedAt:    now.UnixMilli(),
		SoftExpireAt: now.Add(ttl).UnixMilli(),
		HardExpireAt: now.Add(ttl).UnixMilli(),
		Negative:     true,
		KeyHash:      hashHex([]byte(req.Key)),
		TagHashes:    tagHashes(req.Tags),
		NamespaceVer: v.Namespace,
		KeyVersion:   v.Key,
		TagVersions:  tagVersionMap(req.Tags, v),
		StorageHash:  fingerprint(storageKey),
	}
	data, err := encodeRecord(rec)
	if err != nil {
		observe(c.observer, "encode", req.Name, time.Now(), "error", err)
		return err
	}
	if err := c.store.Set(ctx, storageKey, data, ttlWithJitter(storageKey, ttl)); err != nil {
		return err
	}
	c.local.Set(storageKey, data, int64(len(data)), ttl)
	c.index.add(req.Key, req.Tags, storageKey, now.Add(ttl))
	return nil
}

func refresh[T any](c *Client, req GetRequest[T], old versions) {
	select {
	case c.refreshSem <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-c.refreshSem }()
		ctx, cancel := context.WithTimeout(c.ctx, c.limits.RefreshTimeout)
		defer cancel()
		req.Policy.ForceRefresh = true
		_, err := loadAndStore(ctx, c, req, c.storageKey(req.Key, req.Tags, old, req.Codec), old)
		if err != nil && !errors.Is(err, context.Canceled) {
			observe(c.observer, "refresh", req.Name, time.Now(), "error", err)
		}
	}()
}
