# cache design

## Product model

`cache` is one deep method-cache module, not a collection of per-domain cache
objects. A `Client` owns infrastructure. `Get` and `GetMany` describe individual
read-through operations.

The public interface is intentionally small:

```text
New
Get
GetMany
Invalidate
InvalidateTags
InvalidateAll
Close
```

`Tag` is the only dependency-group term. There is no `Scope` alias and no
legacy object-copy API.

## Read path

```text
logical key + Go value type + schema + codec + generations
                           |
                           v
                     physical key
                           |
              +------------+------------+
              |                         |
             L1                        L2
              |                         |
              +------------+------------+
                           |
                 per-process flight
                           |
                        loader
```

L1 and L2 store the same immutable encoded record. The record has a fixed
22-byte binary header containing format version, flags, soft expiry, and hard
expiry. The remaining bytes are the caller codec payload.

The physical key already authenticates namespace, logical key, value type,
schema, codec, tags, and generations. Repeating those maps and hashes inside the
record would add allocations without adding meaningful collision protection.

## Type isolation

The stable Go value type is part of route identity and therefore part of both
the physical key and flight key. Two calls using the same logical key with
different `T` never share storage or loader results. Runtime assertions still
return `ErrTypeMismatch` rather than panicking if an internal invariant is ever
broken.

TTL, negative TTL, stale policy, and stale duration also participate in route
identity. Changing a call site's cache policy cannot accidentally inherit an
entry written under the previous policy.

`WithSchema` is an explicit application-level version. `Codec.Name` is the wire
format version. Both participate in storage identity.

## Invalidation

Redis stores monotonic generations for:

```text
namespace
logical key
dependency tag
```

Physical keys include the current generations. Invalidation only increments
the relevant generation; it never scans Redis and never maintains tag-to-key
sets. Old data becomes unreachable and expires naturally.

Generation keys do not expire. Expiring them can reset a generation to zero and
resurrect an old record after deployment configuration changes. The local
generation cache accepts only equal or higher versions, so delayed events and
out-of-order reads cannot move it backward.

Pub/Sub is optional. Stores may implement `InvalidationBus` to reduce the Fast
mode inconsistency window. Disconnects clear L1 and local versions before
resubscription because Pub/Sub has no replay guarantee.

## Consistency

Fast mode caches generation metadata locally for a short bounded period. A lost
event can delay remote invalidation visibility until that period expires.

Strict mode reads generation metadata from the Store for every operation. It
reduces the visibility window but intentionally trades away most L1 latency
benefit.

Neither mode turns a cache into a source of truth. Applications requiring
linearizable reads should read their database directly.

## Concurrency

The internal flight group is cancellation-independent:

- one detached, timeout-bounded loader runs per effective key;
- each waiter selects on its own context;
- canceling the first waiter does not cancel work needed by other waiters;
- `Close` waits for active loaders and refreshes before closing adapters.

Distributed lock protection is optional through `LockStore`. Lock failures fail
open to the loader because cache coordination must not become an availability
dependency.

## Bounded memory

All process-local structures have explicit limits:

- encoded L1 entries: approximate byte cost;
- route metadata: entry count with sharded LRU eviction;
- generation metadata: entry count, TTL, and sharded LRU eviction;
- background refreshes: semaphore;
- in-flight loads: only currently active keys.

There is no logical-key-to-physical-key index. Generation changes already make
old physical keys unreachable, and an eager deletion index would need eviction
callbacks to avoid becoming an unbounded second cache.

## Failure behavior

FailOpen is the default. Version or L2 read failures execute the loader;
successful loader results are returned even if cache writes fail. FailClosed is
available for specialized contracts.

Malformed records are deleted and treated as misses. Loader errors are returned
unless a caller explicitly enabled stale-if-error. `ErrNotFound` is the only
error cached negatively.

## Dependencies

The runtime module depends only on redigo. JSON, the local cache, the binary
record format, metrics seams, and lifecycle management use the standard
library. Protobuf, msgpack, OpenTelemetry, and alternative L1 implementations
belong in optional adapters rather than the core module.
