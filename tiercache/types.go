package tiercache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
)

type Client struct {
	namespace string

	store Store
	local LocalCache
	codec Codec

	defaults Defaults
	limits   Limits
	failOpen bool
	disabled bool
	stampede StampedeProtection

	observer Observer
	logger   *slog.Logger
	tracer   trace.Tracer

	sf singleflight.Group

	versions *versionCache
	index    *localIndex

	refreshSem chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Key identifies one cached result.
type Key string

// Tag identifies a dependency group. Invalidating a tag invalidates every cached
// result that was read with that tag.
type Tag string

// Scope is an alias for Tag kept for callers that prefer dependency-scope
// language.
type Scope = Tag

// Loader computes a value after a cache miss or refresh.
type Loader[T any] func(context.Context) (T, error)

// BatchLoader computes multiple values after batch cache misses.
type BatchLoader[T any] func(context.Context, []Key) (map[Key]T, error)

// Config describes a Client. Zero values are replaced with production-oriented
// defaults where possible.
type Config struct {
	Namespace string
	Store     Store
	Local     LocalCache
	Codec     Codec

	Defaults Defaults
	Limits   Limits

	FailOpen bool
	Disabled bool
	Stampede StampedeProtection
	Observer Observer
	Logger   *slog.Logger
	Tracer   trace.Tracer
}

// Defaults apply to Get requests that leave those fields unset.
type Defaults struct {
	TTL         time.Duration
	Stale       time.Duration
	NegativeTTL time.Duration
	Policy      Policy
	Consistency Consistency
}

// Limits bounds caller-controlled cache behavior.
type Limits struct {
	MaxTTL             time.Duration
	MaxStale           time.Duration
	RefreshTimeout     time.Duration
	RefreshConcurrency int
	VersionCacheTTL    time.Duration
}

// Policy changes the read-through behavior for one request.
type Policy struct {
	StaleWhileRevalidate bool
	StaleIfError         bool
	ForceRefresh         bool
	NoStore              bool
	Consistency          Consistency
}

// StampedeProtection enables optional lock-backed fill coordination across
// processes. Local singleflight is always enabled.
type StampedeProtection struct {
	Distributed  bool
	LockTTL      time.Duration
	WaitTimeout  time.Duration
	PollInterval time.Duration
}

// Consistency controls when the client trusts its local version cache.
type Consistency int

const (
	ConsistencyDefault Consistency = iota
	ConsistencyFast
	ConsistencyStrict
)

// GetRequest describes one typed read-through cache operation.
type GetRequest[T any] struct {
	Key  Key
	TTL  time.Duration
	Load Loader[T]

	Tags []Tag
	Name string

	Stale       time.Duration
	NegativeTTL time.Duration
	Codec       Codec
	Policy      Policy
}

// BatchItem describes one item in a GetMany request.
type BatchItem struct {
	Key  Key
	Tags []Tag
	Name string
}

// GetManyRequest describes one typed batch read-through cache operation.
type GetManyRequest[T any] struct {
	Items []BatchItem
	Load  BatchLoader[T]

	Name string

	TTL         time.Duration
	Stale       time.Duration
	NegativeTTL time.Duration
	Codec       Codec
	Policy      Policy
}

// BatchResult is the result of GetMany. Values and Errors are keyed by logical
// cache key.
type BatchResult[T any] struct {
	Values map[Key]T
	Errors map[Key]error
}

// Err returns a joined error for all per-key errors.
func (r BatchResult[T]) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	keys := make([]string, 0, len(r.Errors))
	byKey := make(map[string]Key, len(r.Errors))
	for key := range r.Errors {
		s := string(key)
		keys = append(keys, s)
		byKey[s] = key
	}
	sort.Strings(keys)
	errs := make([]error, 0, len(keys))
	for _, s := range keys {
		key := byKey[s]
		errs = append(errs, KeyError{Key: key, Err: r.Errors[key]})
	}
	return errors.Join(errs...)
}

// KeyError associates an error with one logical cache key.
type KeyError struct {
	Key Key
	Err error
}

func (e KeyError) Error() string {
	return fmt.Sprintf("%s: %v", e.Key, e.Err)
}

func (e KeyError) Unwrap() error {
	return e.Err
}

// Codec serializes values stored in L1 and L2 records.
type Codec interface {
	Name() string
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, dst any) error
}

// LocalCache is the bounded in-process cache adapter seam.
type LocalCache interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte, cost int64, ttl time.Duration) bool
	Delete(keys ...string)
	Clear()
	Close() error
}

// Store is the remote record, version, and invalidation adapter seam.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	MGet(ctx context.Context, keys ...string) (map[string][]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, keys ...string) error

	GetVersions(ctx context.Context, names ...string) (map[string]int64, error)
	BumpVersions(ctx context.Context, names ...string) (map[string]int64, error)
	ExpireVersions(ctx context.Context, ttl time.Duration, names ...string) error

	Publish(ctx context.Context, event Invalidation) error
	Subscribe(ctx context.Context, handle func(Invalidation)) error
	Close() error
}

// LockStore is implemented by stores that can coordinate fills across
// processes.
type LockStore interface {
	TryLock(ctx context.Context, name string, ttl time.Duration) (token string, ok bool, err error)
	Unlock(ctx context.Context, name, token string) error
}

// Observer receives low-cardinality cache events.
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

// Invalidation is the Pub/Sub message used to accelerate version updates.
type Invalidation struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
	Timestamp int64  `json:"timestamp"`
}

const (
	invalidationTypeKey   = "key"
	invalidationTypeTag   = "tag"
	invalidationTypeClear = "clear"
)
