# tiercache design

`tiercache` is a typed read-through cache for method results. Its public
interface is intentionally small:

```go
cache, err := tiercache.New(tiercache.Config{...})

value, err := tiercache.Get(ctx, cache, tiercache.GetRequest[T]{...})
result, err := tiercache.GetMany(ctx, cache, tiercache.GetManyRequest[T]{...})

err := cache.Invalidate(ctx, key)
err := cache.InvalidateTags(ctx, tag)
err := cache.InvalidateScope(ctx, scope)
err := cache.InvalidateAll(ctx)
err := cache.Close()
```

The deep module is `Client`: callers describe one cached read, while the
implementation owns L1, L2, encoding, versions, invalidation, stampede
protection, refresh, and observability.

## Upstream ideas absorbed

- Caffeine and Otter: loading belongs in the cache read path; refresh is not
  expiration. A stale value may be returned while one refresh is in flight.
- groupcache: cache fills must be deduplicated. Local singleflight is always on;
  lock-backed cross-process fill coordination is optional.
- Ristretto and Otter: the default local cache should be bounded and optimized
  for contention, not a naked map.
- BigCache and FreeCache: long-lived local cache entries should avoid object
  pointer retention. `tiercache` stores encoded records in both tiers.
- ttlcache and go-cache: the interface should stay obvious for ordinary
  application code. A cached method should read like a normal method call with a
  loader.

## Keys, tags, and invalidation

`Key` identifies one cached result:

```go
tiercache.MakeKey("UserService.GetProfile", userID)
```

`Invalidate(ctx, key)` invalidates only that exact result. Internally it bumps
the key version. No Redis scan is required, and old records naturally disappear
by TTL.

`Tag` identifies a dependency group:

```go
tiercache.MakeTag("user", userID)
```

`InvalidateTags(ctx, tag)` invalidates every cached result that declared the
tag. A profile cache, order list cache, permissions cache, and recommendation
cache can all depend on `user:<id>` without the writer knowing their concrete
keys.

`Scope` is a type alias for `Tag`. It exists for codebases that use scope
language, without creating a second invalidation model.

`InvalidateAll(ctx)` bumps the namespace version. It is a durable clear for
deploys, codec migrations, and emergency rollback. It does not scan Redis.

## Batch reads

`GetMany` exists for call sites that already have a real batch loader. It:

- reads local cache per item
- reads remote cache with `Store.MGet`
- calls one `BatchLoader` for remaining misses
- deduplicates concurrent identical miss batches with local singleflight
- stores loaded values and negative misses per item
- returns `BatchResult.Values` and `BatchResult.Errors`

Partial failures are explicit. `BatchResult.Err()` joins per-key errors using
`errors.Join`, so callers can still use `errors.Is`.

## Version model

The physical storage key is derived from:

- namespace
- namespace version
- logical key
- key version
- sorted tag list
- tag versions
- codec name

Invalidation updates versions in the remote store. Pub/Sub events only update
local version caches early. Correctness does not depend on Pub/Sub delivery.

Invalidation events carry version key names, not raw logical keys or tags. This
keeps application identifiers out of the invalidation channel while still
letting other processes stop using stale local entries.

The record envelope is encoded with msgpack. The caller-selected `Codec` only
controls the payload, so changing the envelope does not change the public
interface.

## Public seams

- `Store`: remote records, versions, and invalidation.
- `LocalCache`: bounded in-process bytes cache.
- `Codec`: typed value serialization through an `any` seam, because `Config`
  cannot hold a generic codec.
- `Observer`: low-cardinality operation observations.
- `MetricsObserver`: maps observations to stable metric names without forcing a
  Prometheus dependency into the core package.
- `LockStore`: optional distributed fill locks.

Each seam has at least two adapters in the codebase before it is public.

## Defaults

The default local adapter is Ristretto. BigCache is available when byte-heavy
workloads need a GC-friendly local cache. The memory adapters exist for tests
and deterministic benchmarks.

The default policy enables stale-while-revalidate and stale-if-error, with fast
consistency. Strict consistency is available for reads that should re-check
versions more aggressively.
