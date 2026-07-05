# tiercache

`tiercache` is a typed read-through hybrid cache for method-level caching.

One process owns one `Client`; each cached method call passes a `GetRequest[T]`
with its key, tags, TTL, and loader.

```go
profile, err := tiercache.Get(ctx, cacheClient, tiercache.GetRequest[*Profile]{
	Name: "UserService.GetProfile",
	Key:  tiercache.MakeKey("UserService.GetProfile", userID),
	TTL:  5 * time.Minute,
	Tags: []tiercache.Tag{
		tiercache.MakeTag("user", userID),
	},
	Load: func(ctx context.Context) (*Profile, error) {
		return repo.GetProfile(ctx, userID)
	},
})
```

Invalidate one concrete result:

```go
err := cacheClient.Invalidate(ctx, tiercache.MakeKey("UserService.GetProfile", userID))
```

Invalidate every result that declared a dependency on a tag:

```go
err := cacheClient.InvalidateTags(ctx, tiercache.MakeTag("user", userID))
```

`Scope` is an alias for `Tag` if that reads better in your domain:

```go
err := cacheClient.InvalidateScope(ctx, tiercache.MakeScope("user", userID))
```

Invalidate the whole namespace without scanning Redis:

```go
err := cacheClient.InvalidateAll(ctx)
```

Batch read-through is available when a real batch loader exists:

```go
result, err := tiercache.GetMany(ctx, cacheClient, tiercache.GetManyRequest[*Profile]{
	Name: "UserService.GetProfiles",
	Items: []tiercache.BatchItem{
		{Key: tiercache.MakeKey("UserService.GetProfile", userID), Tags: []tiercache.Tag{tiercache.MakeTag("user", userID)}},
	},
	TTL: 5 * time.Minute,
	Load: func(ctx context.Context, keys []tiercache.Key) (map[tiercache.Key]*Profile, error) {
		return repo.GetProfilesByCacheKey(ctx, keys)
	},
})
if err != nil {
	return nil, err
}
profile := result.Values[tiercache.MakeKey("UserService.GetProfile", userID)]
```

See [DESIGN.md](DESIGN.md) for the full model and the upstream cache designs
that shaped it.

## Model

- `Key` identifies one cached result.
- `Tag` identifies a dependency group for invalidation.
- `Scope` is an alias for `Tag`.
- `GetRequest[T]` describes one read-through cache operation.
- `GetManyRequest[T]` describes one batch read-through cache operation.
- L1 stores encoded records, not object pointers.
- L2 stores the same encoded records in Redis or another `Store`.
- namespace/key/tag versions are the durable invalidation truth.
- Pub/Sub only accelerates local invalidation.
- local cache is best-effort and bounded.

## Defaults

`New` defaults to:

- Ristretto L1 with a 64 MiB cost budget.
- JSON codec.
- fast consistency with short-lived local version cache.
- stale-while-revalidate and stale-if-error enabled.

Production callers should pass explicit `Config` values for `Namespace`,
`Store`, TTL limits, refresh limits, and observability.

Set `Config.Disabled` during emergency rollback or rollout tests to bypass all
cache reads and writes while still executing loaders.

For lightweight metrics during tests or early rollout:

```go
stats := tiercache.NewStatsObserver()
cacheClient, err := tiercache.New(tiercache.Config{
	Namespace: "myapp",
	Store:     store,
	Observer:  stats,
})
snapshot := stats.Snapshot()
```

For production metrics, adapt `MetricsObserver` to your metrics backend:

```go
cacheClient, err := tiercache.New(tiercache.Config{
	Namespace: "myapp",
	Store:     store,
	Observer:  tiercache.MetricsObserver{Recorder: recorder},
})
```

The built-in metric names include `tiercache_get_total`,
`tiercache_l1_hit_total`, `tiercache_l2_miss_total`,
`tiercache_load_duration_seconds`, `tiercache_invalidate_total`, and
`tiercache_singleflight_shared_total`.

For OpenTelemetry tracing, pass a tracer:

```go
cacheClient, err := tiercache.New(tiercache.Config{
	Namespace: "myapp",
	Store:     store,
	Tracer:    otel.Tracer("myapp/tiercache"),
})
```

The core spans are `tiercache.get`, `tiercache.get_many`,
`tiercache.invalidate`, with child spans for `tiercache.l1`, `tiercache.l2`,
and `tiercache.load` on single-key reads.

## Optional distributed stampede protection

Local singleflight is always enabled per process. To coordinate loader calls
across processes, enable lock-backed stampede protection on a store that
implements distributed locks:

```go
cacheClient, err := tiercache.New(tiercache.Config{
	Namespace: "myapp",
	Store:     tiercache.NewRedisStore(pool.Get, "myapp"),
	Stampede: tiercache.StampedeProtection{
		Distributed: true,
		LockTTL:     3 * time.Second,
		WaitTimeout: 100 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	},
})
```

When another process owns the fill lock, callers poll L2 briefly and reuse the
filled value if it appears. If the lock path fails, `tiercache` falls back to
the loader to preserve availability.
