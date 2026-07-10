# cache

`cache` is a typed, read-through, two-level cache for Go method results.

One process creates one `Client`. Each cached method supplies a logical key,
TTL, and loader. The client owns L1, Redis or another `Store`, request
coalescing, invalidation generations, negative caching, and observability.

```go
store, err := cache.NewRedisStore(redisPool.Get)
if err != nil {
	return err
}

client, err := cache.New(cache.Config{
	Namespace: "myapp",
	Store:     store,
})
if err != nil {
	return err
}
defer client.Close()
```

The built-in Redis adapter targets a standalone Redis or Sentinel-managed
primary through a redigo pool. Redis Cluster deployments should provide a
cluster-aware `Store` that groups multi-key operations by slot.

## Get

```go
func (s *UserService) Profile(ctx context.Context, userID int64) (*Profile, error) {
	key := cache.MakeKey("UserService.Profile", userID)
	userTag := cache.MakeTag("user", userID)

	return cache.Get(ctx, s.cache, key, 5*time.Minute,
		func(ctx context.Context) (*Profile, error) {
			profile, err := s.repo.Profile(ctx, userID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, cache.ErrNotFound
			}
			return profile, err
		},
		cache.WithTags(userTag),
	)
}
```

`Get` checks L1, then L2, then calls the loader. Successful and not-found
results are written back automatically. Concurrent callers for the same
effective key share one loader execution; each waiter can still cancel its own
request independently.

The main call has five required values:

```go
value, err := cache.Get(ctx, client, key, ttl, loader)
```

Optional behavior stays out of the normal path:

```go
cache.WithTags(tags...)
cache.WithNegativeTTL(20 * time.Second)
cache.WithStaleWhileRevalidate(time.Minute)
cache.WithStaleIfError(time.Minute)
cache.WithSchema("v2")
cache.WithConsistency(cache.Strict)
cache.WithForceRefresh()
cache.WithoutStore()
```

## Invalidation

Invalidate exact results when their complete logical key is known:

```go
err := client.Invalidate(ctx, cache.MakeKey("UserService.Profile", userID))
```

Tags express dependencies. A write can invalidate every cached result that
declared the same tag without tracking physical Redis keys:

```go
func (s *UserService) UpdateProfile(ctx context.Context, profile *Profile) error {
	if err := s.repo.UpdateProfile(ctx, profile); err != nil {
		return err
	}
	return s.cache.InvalidateTags(ctx, cache.MakeTag("user", profile.UserID))
}
```

`Invalidate` changes one key generation. `InvalidateTags` changes dependency
generations. `InvalidateAll` changes the namespace generation. Generations are
durable and monotonic; Pub/Sub is only a latency optimization.

Generation metadata intentionally does not expire because resetting a version
can resurrect an older record. Capacity planning should account for the number
of distinct keys and tags that are explicitly invalidated; prefer a bounded tag
vocabulary for broad invalidation.

## Batch loading

Use `GetMany` only when the backend has a real batch operation:

```go
result, err := cache.GetMany(ctx, client, []cache.Item[int64]{
	{ID: 1, Key: cache.MakeKey("UserService.Profile", 1), Tags: []cache.Tag{cache.MakeTag("user", 1)}},
	{ID: 2, Key: cache.MakeKey("UserService.Profile", 2), Tags: []cache.Tag{cache.MakeTag("user", 2)}},
}, 5*time.Minute, func(ctx context.Context, userIDs []int64) (map[int64]*Profile, error) {
	return repo.Profiles(ctx, userIDs)
})
if err != nil {
	return nil, err
}
```

`BatchResult.Values` and `BatchResult.Errors` are indexed by business ID, not
cache key. `BatchResult.Err()` joins per-ID errors in deterministic order.

## Safety defaults

- A non-empty namespace is required.
- Cache infrastructure failures fail open by default; use `FailClosed` only
  when cache availability is part of the business contract.
- Stale responses are opt-in.
- Not-found caching is opt-in through `Defaults.NegativeTTL` or the per-call
  `WithNegativeTTL` option.
- Logical TTLs are jittered downward by up to ten percent.
- Value type, codec name, and optional schema are part of the physical key.
- L1, route metadata, and local generation metadata are all bounded.
- Encoded records are immutable, so callers cannot mutate cached objects.

Use `WithSchema("v2")` when changing a type's wire-compatible meaning without
changing its Go type. A codec's `Name` must also change when its wire format
changes incompatibly.

## Local cache

The default L1 is a dependency-free, 64-shard bounded LRU with a 64 MiB budget.
Override it when needed:

```go
cache.NewLocalCache(256 << 20)
cache.NewNoopLocal()
```

Custom implementations can satisfy `LocalCache`. The byte slices returned by
`Get` are immutable and must not be modified.

## Observability

Pass an `Observer` for metrics, tracing, or structured event recording. Event
names and cache names are low cardinality; complete logical keys are never sent
to the observer.

```go
stats := cache.NewStatsObserver()
client, err := cache.New(cache.Config{
	Namespace: "myapp",
	Store:     store,
	Observer:  stats,
})
```

`MetricsObserver` adapts the event stream to a small `MetricRecorder` interface.
This keeps Prometheus and OpenTelemetry dependencies out of the core module.

## Rollout bypass

Disabled mode needs no store and always executes loaders:

```go
client, err := cache.New(cache.Config{
	Namespace: "myapp",
	Disabled:  true,
})
```

See [DESIGN.md](DESIGN.md) for invariants, consistency behavior, and internal
architecture.
