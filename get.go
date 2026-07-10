package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Get returns a cached value or calls load exactly once per process and
// effective storage key.
func Get[T any](ctx context.Context, c *Client, key Key, ttl time.Duration, load Loader[T], options ...GetOption) (value T, err error) {
	if err = c.begin(); err != nil {
		return value, err
	}
	defer c.end()
	req, err := makeRequest(c, key, ttl, load, options)
	if err != nil {
		return value, err
	}
	start := time.Now()
	result := ""
	defer func() { observe(c.observer, "get", req.name, start, result, err) }()
	if err = ctx.Err(); err != nil {
		return value, err
	}
	if c.disabled {
		value, err = callLoader(ctx, c, req)
		if err == nil && isNil(value) {
			err = ErrNoValue
		}
		result = "disabled"
		return value, err
	}

	versions, versionErr := c.loadVersions(ctx, req.route, req.consistency)
	if versionErr != nil {
		if c.failureMode == FailClosed {
			return value, versionErr
		}
		result = "bypass"
		return loadBypass(ctx, c, req)
	}
	storageKey := storageKey(c, req.route, versions)

	var staleValue T
	haveStale := false
	skipRemoteCheck := false
	if !req.force {
		cached, state, hit, cacheErr := readLocal(c, req, storageKey)
		if hit {
			switch {
			case cacheErr != nil:
				result = "l1_negative"
				return value, cacheErr
			case state == recordFresh:
				result = "l1_hit"
				return cached, nil
			default:
				staleValue, haveStale = cached, true
				if req.staleWhileRevalidate {
					refresh(c, ctx, req)
					result = "l1_stale"
					return cached, nil
				}
			}
		}

		cached, state, hit, cacheErr, infrastructureErr := readRemote(ctx, c, req, storageKey)
		if infrastructureErr != nil {
			if haveStale && req.staleIfErr {
				result = "stale_if_error"
				return staleValue, nil
			}
			if c.failureMode == FailClosed {
				return value, infrastructureErr
			}
			skipRemoteCheck = true
		} else if hit {
			switch {
			case cacheErr != nil:
				result = "l2_negative"
				return value, cacheErr
			case state == recordFresh:
				result = "l2_hit"
				return cached, nil
			default:
				staleValue, haveStale = cached, true
				if req.staleWhileRevalidate {
					refresh(c, ctx, req)
					result = "l2_stale"
					return cached, nil
				}
			}
		}
	}

	value, err = loadAndStore(ctx, c, req, storageKey, skipRemoteCheck)
	if err != nil && haveStale && req.staleIfErr {
		result = "stale_if_error"
		return staleValue, nil
	}
	if err != nil {
		result = "load_error"
		return value, err
	}
	result = "load"
	return value, nil
}

func makeRequest[T any](c *Client, key Key, ttl time.Duration, load Loader[T], options []GetOption) (request[T], error) {
	if err := validateKey(key); err != nil {
		return request[T]{}, err
	}
	if load == nil {
		return request[T]{}, ErrNoLoader
	}
	var opts getOptions
	for _, option := range options {
		if option != nil {
			option.apply(&opts)
		}
	}
	if ttl == 0 {
		ttl = c.defaults.TTL
	}
	if !opts.negativeTTLSet {
		opts.negativeTTL = c.defaults.NegativeTTL
	}
	if opts.codec == nil {
		opts.codec = c.codec
	}
	if !opts.consistencySet {
		opts.consistency = c.consistency
	}
	opts.tags = canonicalTags(opts.tags)
	if err := validateTags(opts.tags); err != nil {
		return request[T]{}, err
	}
	if len(opts.tags) > c.limits.MaxTagsPerEntry {
		return request[T]{}, fmt.Errorf("%w: too many tags", ErrInvalidConfig)
	}
	if opts.staleIfError && opts.staleWhileRevalidate {
		return request[T]{}, fmt.Errorf("%w: choose one stale policy", ErrInvalidConfig)
	}
	if ttl < time.Millisecond || ttl > c.limits.MaxTTL || opts.negativeTTL < 0 ||
		opts.negativeTTL > c.limits.MaxTTL || opts.stale < 0 || opts.stale > c.limits.MaxStale {
		return request[T]{}, ErrInvalidTTL
	}
	if (opts.staleIfError || opts.staleWhileRevalidate) && opts.stale < time.Millisecond {
		return request[T]{}, ErrInvalidTTL
	}
	if opts.codec == nil || strings.TrimSpace(opts.codec.Name()) == "" {
		return request[T]{}, ErrInvalidCodec
	}
	if len(opts.codec.Name()) > 128 || len(opts.schema) > 128 {
		return request[T]{}, ErrInvalidConfig
	}
	if opts.consistency != Fast && opts.consistency != Strict {
		return request[T]{}, fmt.Errorf("%w: unknown consistency", ErrInvalidConfig)
	}
	valueType := cachedTypeID[T]()
	schema := strings.TrimSpace(opts.schema)
	staleMode := uint8(0)
	if opts.staleWhileRevalidate {
		staleMode = 1
	} else if opts.staleIfError {
		staleMode = 2
	}
	route := c.routeFor(key, valueType, schema, opts.codec, opts.tags, ttl, opts.stale, opts.negativeTTL, staleMode)
	return request[T]{
		key:                  key,
		name:                 route.name,
		typeID:               valueType,
		route:                route,
		ttl:                  ttl,
		load:                 load,
		tags:                 opts.tags,
		stale:                opts.stale,
		negativeTTL:          opts.negativeTTL,
		codec:                opts.codec,
		consistency:          opts.consistency,
		force:                opts.forceRefresh,
		noStore:              opts.noStore,
		staleIfErr:           opts.staleIfError,
		staleWhileRevalidate: opts.staleWhileRevalidate,
		schema:               schema,
	}, nil
}

func readLocal[T any](c *Client, req request[T], storageKey string) (T, recordState, bool, error) {
	var zero T
	data, ok := c.local.Get(storageKey)
	if !ok {
		observe(c.observer, "l1", req.name, time.Now(), "miss", nil)
		return zero, recordExpired, false, nil
	}
	value, state, _, err := decodeValue(req, data, time.Now())
	if errors.Is(err, ErrNotFound) {
		observe(c.observer, "l1", req.name, time.Now(), "negative", nil)
		return zero, state, true, err
	}
	if err != nil || state == recordExpired {
		c.local.Delete(storageKey)
		observe(c.observer, "l1", req.name, time.Now(), "miss", nil)
		return zero, recordExpired, false, nil
	}
	observe(c.observer, "l1", req.name, time.Now(), "hit", nil)
	return value, state, true, nil
}

func readRemote[T any](ctx context.Context, c *Client, req request[T], storageKey string) (T, recordState, bool, error, error) {
	var zero T
	data, err := c.store.Get(ctx, storageKey)
	if errors.Is(err, ErrNotFound) {
		observe(c.observer, "l2", req.name, time.Now(), "miss", nil)
		return zero, recordExpired, false, nil, nil
	}
	if err != nil {
		observe(c.observer, "l2", req.name, time.Now(), "error", err)
		return zero, recordExpired, false, nil, err
	}
	value, state, ttl, decodeErr := decodeValue(req, data, time.Now())
	if errors.Is(decodeErr, ErrNotFound) {
		c.local.Set(storageKey, data, int64(len(data)), ttl)
		observe(c.observer, "l2", req.name, time.Now(), "negative", nil)
		return zero, state, true, decodeErr, nil
	}
	if decodeErr != nil || state == recordExpired {
		_ = c.store.Delete(ctx, storageKey)
		observe(c.observer, "decode", req.name, time.Now(), "error", decodeErr)
		return zero, recordExpired, false, nil, nil
	}
	c.local.Set(storageKey, data, int64(len(data)), ttl)
	observe(c.observer, "l2", req.name, time.Now(), "hit", nil)
	return value, state, true, nil, nil
}

func decodeValue[T any](req request[T], data []byte, now time.Time) (T, recordState, time.Duration, error) {
	var zero T
	record, err := decodeRecord(data)
	if err != nil {
		return zero, recordExpired, 0, err
	}
	state := record.state(now)
	ttl := record.ttl(now)
	if state == recordExpired {
		return zero, state, 0, nil
	}
	if record.negative {
		return zero, state, ttl, ErrNotFound
	}
	var value T
	if err := req.codec.Unmarshal(record.payload, &value); err != nil {
		return zero, state, ttl, err
	}
	if isNil(value) {
		return zero, state, ttl, ErrNoValue
	}
	return value, state, ttl, nil
}

func loadBypass[T any](ctx context.Context, c *Client, req request[T]) (T, error) {
	key := "bypass:" + hashHex([]byte(c.namespace+"|"+string(req.key)+"|"+req.typeID+"|"+req.schema))
	return runFlight(ctx, c, key, req, func(loadCtx context.Context) (T, error) {
		return callLoader(loadCtx, c, req)
	})
}

func loadAndStore[T any](ctx context.Context, c *Client, req request[T], storageKey string, skipRemote bool) (T, error) {
	flightKey := storageKey
	if req.noStore {
		flightKey += ":no-store"
	}
	return runFlight(ctx, c, flightKey, req, func(loadCtx context.Context) (T, error) {
		if !req.force && !skipRemote {
			value, state, hit, cacheErr, infrastructureErr := readRemote(loadCtx, c, req, storageKey)
			if infrastructureErr != nil && c.failureMode == FailClosed {
				var zero T
				return zero, infrastructureErr
			}
			if hit && (cacheErr != nil || state == recordFresh) {
				return value, cacheErr
			}
		}
		value, err := loadWithStampedeProtection(loadCtx, c, req, storageKey)
		if err != nil {
			if errors.Is(err, ErrNotFound) && !req.noStore && req.negativeTTL > 0 {
				if storeErr := storeNegative(loadCtx, c, req, storageKey); storeErr != nil {
					observe(c.observer, "l2_set", req.name, time.Now(), "error", storeErr)
				}
			}
			var zero T
			return zero, err
		}
		if isNil(value) {
			var zero T
			return zero, ErrNoValue
		}
		if !req.noStore {
			if storeErr := storeValue(loadCtx, c, req, storageKey, value); storeErr != nil {
				observe(c.observer, "l2_set", req.name, time.Now(), "error", storeErr)
			}
		}
		return value, nil
	})
}

func runFlight[T any](ctx context.Context, c *Client, key string, req request[T], fn func(context.Context) (T, error)) (T, error) {
	var zero T
	value, err, shared := c.flights.do(ctx, key,
		func() { c.workers.Add(1) },
		func() { c.workers.Done() },
		func() (any, error) {
			loadCtx, cancel := c.loaderContext(ctx, c.limits.LoadTimeout)
			defer cancel()
			return fn(loadCtx)
		},
	)
	if shared {
		observe(c.observer, "singleflight", req.name, time.Now(), "shared", nil)
	}
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return zero, ErrTypeMismatch
	}
	if isNil(typed) {
		return zero, ErrNoValue
	}
	return typed, nil
}

func (c *Client) loaderContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func loadWithStampedeProtection[T any](ctx context.Context, c *Client, req request[T], storageKey string) (T, error) {
	if !c.stampede.Distributed {
		return callLoader(ctx, c, req)
	}
	locks := c.store.(LockStore)
	lockName := c.namespace + ":lock:" + hashHex([]byte(storageKey))
	token, acquired, err := locks.TryLock(ctx, lockName, c.stampede.LockTTL)
	if err != nil {
		observe(c.observer, "lock", req.name, time.Now(), "error", err)
		return callLoader(ctx, c, req)
	}
	if acquired {
		defer unlock(c, locks, lockName, token, req.name)
		return loadAfterLock(ctx, c, req, storageKey)
	}
	waitCtx, cancel := context.WithTimeout(ctx, c.stampede.WaitTimeout)
	defer cancel()
	ticker := time.NewTicker(c.stampede.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			token, acquired, err = locks.TryLock(ctx, lockName, c.stampede.LockTTL)
			if err == nil && acquired {
				defer unlock(c, locks, lockName, token, req.name)
				return loadAfterLock(ctx, c, req, storageKey)
			}
			return callLoader(ctx, c, req)
		case <-ticker.C:
			value, state, hit, cacheErr, _ := readRemote(waitCtx, c, req, storageKey)
			if hit && (cacheErr != nil || state == recordFresh) {
				return value, cacheErr
			}
		}
	}
}

func loadAfterLock[T any](ctx context.Context, c *Client, req request[T], storageKey string) (T, error) {
	if !req.force {
		value, state, hit, cacheErr, _ := readRemote(ctx, c, req, storageKey)
		if hit && (cacheErr != nil || state == recordFresh) {
			return value, cacheErr
		}
	}
	return callLoader(ctx, c, req)
}

func unlock(c *Client, locks LockStore, name, token, metricName string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := locks.Unlock(ctx, name, token); err != nil {
		observe(c.observer, "unlock", metricName, time.Now(), "error", err)
	}
}

func callLoader[T any](ctx context.Context, c *Client, req request[T]) (T, error) {
	start := time.Now()
	value, err := req.load(ctx)
	result := "success"
	if errors.Is(err, ErrNotFound) {
		result = "not_found"
	} else if err != nil {
		result = "error"
	}
	observe(c.observer, "load", req.name, start, result, err)
	return value, err
}

func storeValue[T any](ctx context.Context, c *Client, req request[T], storageKey string, value T) error {
	payload, err := req.codec.Marshal(value)
	if err != nil {
		observe(c.observer, "encode", req.name, time.Now(), "error", err)
		return err
	}
	now := time.Now()
	freshTTL := jitteredTTL(storageKey, req.ttl)
	hardTTL := freshTTL + req.stale
	data := encodeRecord(record{
		softExpireAt: now.Add(freshTTL).UnixMilli(),
		hardExpireAt: now.Add(hardTTL).UnixMilli(),
		payload:      payload,
	})
	if err := c.store.Set(ctx, storageKey, data, hardTTL); err != nil {
		return err
	}
	c.local.Set(storageKey, data, int64(len(data)), hardTTL)
	return nil
}

func storeNegative[T any](ctx context.Context, c *Client, req request[T], storageKey string) error {
	ttl := jitteredTTL(storageKey, req.negativeTTL)
	now := time.Now()
	data := encodeRecord(record{
		softExpireAt: now.Add(ttl).UnixMilli(),
		hardExpireAt: now.Add(ttl).UnixMilli(),
		negative:     true,
	})
	if err := c.store.Set(ctx, storageKey, data, ttl); err != nil {
		return err
	}
	c.local.Set(storageKey, data, int64(len(data)), ttl)
	return nil
}

func refresh[T any](c *Client, parent context.Context, req request[T]) {
	select {
	case c.refresh <- struct{}{}:
	default:
		return
	}
	if err := c.begin(); err != nil {
		<-c.refresh
		return
	}
	go func() {
		defer c.end()
		defer func() { <-c.refresh }()
		ctx, cancel := c.loaderContext(parent, c.limits.RefreshTimeout)
		defer cancel()
		versions, err := c.loadVersions(ctx, req.route, req.consistency)
		if err != nil {
			observe(c.observer, "refresh", req.name, time.Now(), "error", err)
			return
		}
		req.force = true
		_, err = loadAndStore(ctx, c, req, storageKey(c, req.route, versions), false)
		if err != nil && !errors.Is(err, context.Canceled) {
			observe(c.observer, "refresh", req.name, time.Now(), "error", err)
		}
	}()
}

func lockToken() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}
