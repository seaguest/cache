package cache

import (
	"context"
	"log/slog"
	"time"
)

// Key identifies one cached result.
type Key string

// String returns the logical key text.
func (k Key) String() string { return string(k) }

// Tag identifies a dependency shared by one or more cached results.
type Tag string

// Loader computes a value when it is not available from the cache.
type Loader[T any] func(context.Context) (T, error)

// BatchLoader computes all requested business IDs in one operation. Missing
// IDs are treated as ErrNotFound.
type BatchLoader[ID comparable, T any] func(context.Context, []ID) (map[ID]T, error)

// Config configures a Client. Namespace and Store are required unless Disabled
// is true.
type Config struct {
	Namespace string
	Store     Store
	Local     LocalCache
	Codec     Codec

	Defaults Defaults
	Limits   Limits

	FailureMode FailureMode
	Consistency Consistency
	Disabled    bool
	Stampede    StampedeProtection
	Observer    Observer
	Logger      *slog.Logger
}

// Defaults apply when a call does not override the corresponding behavior.
type Defaults struct {
	TTL         time.Duration
	NegativeTTL time.Duration
}

// Limits bound caller-controlled work and background activity.
type Limits struct {
	MaxTTL              time.Duration
	MaxStale            time.Duration
	LoadTimeout         time.Duration
	RefreshTimeout      time.Duration
	RefreshConcurrency  int
	VersionCacheTTL     time.Duration
	VersionCacheEntries int
	RouteCacheEntries   int
	MaxTagsPerEntry     int
	MaxBatchSize        int
}

// FailureMode controls behavior when version metadata cannot be read.
type FailureMode uint8

const (
	// FailOpen bypasses the cache and calls the loader. This is the default.
	FailOpen FailureMode = iota
	// FailClosed returns the cache infrastructure error.
	FailClosed
)

// Consistency controls how often version metadata is checked remotely.
type Consistency uint8

const (
	// Fast trusts the short-lived local version cache. This is the default.
	Fast Consistency = iota
	// Strict reads current versions from the Store on every operation.
	Strict
)

// StampedeProtection enables optional cross-process fill coordination. Local
// singleflight is always enabled.
type StampedeProtection struct {
	Distributed  bool
	LockTTL      time.Duration
	WaitTimeout  time.Duration
	PollInterval time.Duration
}

// Item describes one entry in a GetMany call. ID is passed to the loader and
// indexes the result; Key and Tags are cache metadata.
type Item[ID comparable] struct {
	ID   ID
	Key  Key
	Tags []Tag
}

// BatchResult contains values and per-ID errors from GetMany.
type BatchResult[ID comparable, T any] struct {
	Values map[ID]T
	Errors map[ID]error
	order  []ID
}

// Err joins all per-ID errors in deterministic order.
func (r BatchResult[ID, T]) Err() error {
	return joinItemErrors(r.Errors, r.order)
}

// Codec serializes values stored in L1 and L2. Name must be stable and should
// change whenever the wire format changes incompatibly.
type Codec interface {
	Name() string
	Marshal(any) ([]byte, error)
	Unmarshal([]byte, any) error
}

// LocalCache stores immutable encoded entries in process. Implementations must
// be bounded. Returned byte slices are treated as read-only.
type LocalCache interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, cost int64, ttl time.Duration) bool
	Delete(keys ...string)
	Clear()
	Close() error
}

// Store is the durable cache and generation store.
type Store interface {
	Get(context.Context, string) ([]byte, error)
	MGet(context.Context, ...string) (map[string][]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
	Delete(context.Context, ...string) error

	GetVersions(context.Context, ...string) (map[string]int64, error)
	BumpVersions(context.Context, ...string) (map[string]int64, error)
	Close() error
}

// InvalidationBus is an optional Store capability. Generations remain the
// source of truth; events only reduce invalidation latency.
type InvalidationBus interface {
	Publish(context.Context, string, Invalidation) error
	Subscribe(context.Context, string, func(Invalidation)) error
}

// LockStore is an optional Store capability for distributed fill protection.
type LockStore interface {
	TryLock(context.Context, string, time.Duration) (token string, acquired bool, err error)
	Unlock(context.Context, string, string) error
}

// Observer receives synchronous, low-cardinality operation observations.
type Observer interface {
	Observe(Observation)
}

// Observation describes one completed cache operation.
type Observation struct {
	Operation string
	Name      string
	Result    string
	Duration  time.Duration
	Err       error
}

// Invalidation is the optional cross-process version notification.
type Invalidation struct {
	Kind    InvalidationKind `json:"kind"`
	Name    string           `json:"name"`
	Version int64            `json:"version"`
}

// InvalidationKind identifies what changed.
type InvalidationKind string

const (
	InvalidationKey       InvalidationKind = "key"
	InvalidationTag       InvalidationKind = "tag"
	InvalidationNamespace InvalidationKind = "namespace"
)

type getOptions struct {
	tags                 []Tag
	stale                time.Duration
	staleWhileRevalidate bool
	negativeTTL          time.Duration
	negativeTTLSet       bool
	codec                Codec
	consistency          Consistency
	consistencySet       bool
	forceRefresh         bool
	noStore              bool
	staleIfError         bool
	schema               string
}

// GetOption customizes a Get or GetMany call.
type GetOption interface{ apply(*getOptions) }

type tagsOption []Tag

func (v tagsOption) apply(o *getOptions) {
	if len(o.tags) == 0 {
		o.tags = v
		return
	}
	o.tags = append(o.tags, v...)
}

type staleWhileRevalidateOption time.Duration

func (v staleWhileRevalidateOption) apply(o *getOptions) {
	o.stale = time.Duration(v)
	o.staleWhileRevalidate = true
}

type staleIfErrorOption time.Duration

func (v staleIfErrorOption) apply(o *getOptions) {
	o.stale = time.Duration(v)
	o.staleIfError = true
}

type negativeTTLOption time.Duration

func (v negativeTTLOption) apply(o *getOptions) {
	o.negativeTTL = time.Duration(v)
	o.negativeTTLSet = true
}

type codecOption struct{ codec Codec }

func (v codecOption) apply(o *getOptions) { o.codec = v.codec }

type schemaOption string

func (v schemaOption) apply(o *getOptions) { o.schema = string(v) }

type consistencyOption Consistency

func (v consistencyOption) apply(o *getOptions) {
	o.consistency = Consistency(v)
	o.consistencySet = true
}

type forceRefreshOption struct{}

func (forceRefreshOption) apply(o *getOptions) { o.forceRefresh = true }

type noStoreOption struct{}

func (noStoreOption) apply(o *getOptions) { o.noStore = true }

// WithTags declares dependencies used by InvalidateTags.
func WithTags(tags ...Tag) GetOption {
	return tagsOption(tags)
}

// WithStaleWhileRevalidate returns stale values for d while one caller refreshes
// the entry in the background.
func WithStaleWhileRevalidate(d time.Duration) GetOption {
	return staleWhileRevalidateOption(d)
}

// WithStaleIfError retains stale values for d and returns one only when the
// synchronous loader fails.
func WithStaleIfError(d time.Duration) GetOption {
	return staleIfErrorOption(d)
}

// WithNegativeTTL overrides the not-found cache duration. Zero disables it.
func WithNegativeTTL(d time.Duration) GetOption {
	return negativeTTLOption(d)
}

// WithCodec overrides the client codec for this call.
func WithCodec(codec Codec) GetOption {
	return codecOption{codec: codec}
}

// WithSchema adds an explicit application schema version to the storage key.
func WithSchema(schema string) GetOption {
	return schemaOption(schema)
}

// WithConsistency overrides the client consistency mode.
func WithConsistency(consistency Consistency) GetOption {
	return consistencyOption(consistency)
}

// WithForceRefresh bypasses cached values while retaining singleflight.
func WithForceRefresh() GetOption {
	return forceRefreshOption{}
}

// WithoutStore executes the read-through flow without writing the result.
func WithoutStore() GetOption {
	return noStoreOption{}
}

type request[T any] struct {
	key                  Key
	name                 string
	typeID               string
	route                *route
	ttl                  time.Duration
	load                 Loader[T]
	tags                 []Tag
	stale                time.Duration
	negativeTTL          time.Duration
	codec                Codec
	consistency          Consistency
	force                bool
	noStore              bool
	staleIfErr           bool
	staleWhileRevalidate bool
	schema               string
}
