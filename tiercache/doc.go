// Package tiercache provides a typed read-through two-tier cache for method
// results.
//
// A Client owns the cache infrastructure. Each Get or GetMany call describes a
// logical key, TTL, dependency tags, and a loader. The implementation handles
// local cache, remote cache, encoding, negative cache, stale refresh,
// invalidation versions, stampede protection, and observability.
package tiercache
