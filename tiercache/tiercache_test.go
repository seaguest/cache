package tiercache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gomodule/redigo/redis"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type testValue struct {
	ID   int
	Name string
}

type recordingStore struct {
	*MemoryStore
	events chan Invalidation
}

func newRecordingStore() *recordingStore {
	return &recordingStore{
		MemoryStore: NewMemoryStore(),
		events:      make(chan Invalidation, 8),
	}
}

func (s *recordingStore) Publish(ctx context.Context, event Invalidation) error {
	s.events <- event
	return s.MemoryStore.Publish(ctx, event)
}

type failingStore struct {
	Store
	getErr  error
	mgetErr error
	setErr  error
}

func (s failingStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.Store.Get(ctx, key)
}

func (s failingStore) MGet(ctx context.Context, keys ...string) (map[string][]byte, error) {
	if s.mgetErr != nil {
		return nil, s.mgetErr
	}
	return s.Store.MGet(ctx, keys...)
}

func (s failingStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.setErr != nil {
		return s.setErr
	}
	return s.Store.Set(ctx, key, value, ttl)
}

type recordingMetrics struct {
	mu         sync.Mutex
	counters   map[string]uint64
	histograms map[string]uint64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		counters:   make(map[string]uint64),
		histograms: make(map[string]uint64),
	}
}

func (m *recordingMetrics) IncCounter(name string, _ MetricLabels) {
	m.mu.Lock()
	m.counters[name]++
	m.mu.Unlock()
}

func (m *recordingMetrics) ObserveHistogram(name string, _ float64, _ MetricLabels) {
	m.mu.Lock()
	m.histograms[name]++
	m.mu.Unlock()
}

func (m *recordingMetrics) counter(name string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[name]
}

func (m *recordingMetrics) histogram(name string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.histograms[name]
}

func newTestClient(t *testing.T, store Store) *Client {
	t.Helper()
	if store == nil {
		store = NewMemoryStore()
	}
	c, err := New(Config{
		Namespace: "test",
		Store:     store,
		Local:     NewMemoryLocal(1 << 20),
		Codec:     JSONCodec{},
		Defaults: Defaults{
			TTL:         time.Second,
			Stale:       time.Second,
			NegativeTTL: time.Second,
			Policy: Policy{
				StaleWhileRevalidate: true,
				StaleIfError:         true,
			},
			Consistency: ConsistencyFast,
		},
		Limits: Limits{
			MaxTTL:             time.Hour,
			MaxStale:           time.Hour,
			RefreshTimeout:     time.Second,
			RefreshConcurrency: 8,
			VersionCacheTTL:    10 * time.Millisecond,
		},
		FailOpen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestGetUsesLocalCache(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name: "TestGetUsesLocalCache",
		Key:  MakeKey("value", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			loads.Add(1)
			return &testValue{ID: 1, Name: "one"}, nil
		},
	}
	got, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "one" {
		t.Fatalf("got %q", got.Name)
	}
	got, err = Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "one" {
		t.Fatalf("got %q", got.Name)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestRemoteHitBackfillsLocal(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	c1 := newTestClient(t, store)
	c2 := newTestClient(t, store)
	key := MakeKey("remote-hit", 1)

	_, err := Get(ctx, c1, GetRequest[*testValue]{
		Name: "remote-hit",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "from-loader"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var loads atomic.Int64
	req := GetRequest[*testValue]{
		Name: "remote-hit",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			loads.Add(1)
			return &testValue{ID: 2, Name: "unexpected"}, nil
		},
	}
	got, err := Get(ctx, c2, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "from-loader" {
		t.Fatalf("got %q", got.Name)
	}
	got, err = Get(ctx, c2, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "from-loader" {
		t.Fatalf("got %q", got.Name)
	}
	if loads.Load() != 0 {
		t.Fatalf("loader called on remote/local hit")
	}
}

func TestRemoteGetFailureFallsBackToLoader(t *testing.T) {
	ctx := context.Background()
	store := failingStore{
		Store:  NewMemoryStore(),
		getErr: errors.New("remote get failed"),
	}
	c := newTestClient(t, store)
	var loads atomic.Int64

	got, err := Get(ctx, c, GetRequest[*testValue]{
		Name: "remote-get-fail",
		Key:  MakeKey("remote-get-fail", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			loads.Add(1)
			return &testValue{ID: 1, Name: "loaded"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "loaded" || loads.Load() != 1 {
		t.Fatalf("unexpected result: got=%#v loads=%d", got, loads.Load())
	}
}

func TestRemoteSetFailureStillReturnsLoadedValue(t *testing.T) {
	ctx := context.Background()
	store := failingStore{
		Store:  NewMemoryStore(),
		setErr: errors.New("remote set failed"),
	}
	c := newTestClient(t, store)

	got, err := Get(ctx, c, GetRequest[*testValue]{
		Name: "remote-set-fail",
		Key:  MakeKey("remote-set-fail", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "loaded"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "loaded" {
		t.Fatalf("got %q", got.Name)
	}
}

func TestCorruptRemoteRecordReloads(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	c := newTestClient(t, store)
	key := MakeKey("corrupt-remote", 1)
	req := GetRequest[*testValue]{
		Name: "corrupt-remote",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "loaded"}, nil
		},
	}
	req = applyDefaults(c, req)
	if req.Codec == nil {
		req.Codec = c.codec
	}
	v, err := c.loadVersions(ctx, key, nil, ConsistencyFast)
	if err != nil {
		t.Fatal(err)
	}
	storageKey := c.storageKey(key, nil, v, req.Codec)
	if err := store.Set(ctx, storageKey, []byte("not-a-record"), time.Minute); err != nil {
		t.Fatal(err)
	}

	got, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "loaded" {
		t.Fatalf("got %q", got.Name)
	}
	stored, err := store.Get(ctx, storageKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRecord(stored); err != nil {
		t.Fatalf("record was not replaced: %v", err)
	}
}

func TestGetManyLoadsOnlyMisses(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	c1 := newTestClient(t, store)
	c2 := newTestClient(t, store)
	k1 := MakeKey("many", 1)
	k2 := MakeKey("many", 2)

	if _, err := Get(ctx, c1, GetRequest[*testValue]{
		Name: "many",
		Key:  k1,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "one"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	var loaded []Key
	got, err := GetMany(ctx, c2, GetManyRequest[*testValue]{
		Name: "many",
		Items: []BatchItem{
			{Key: k1, Name: "many"},
			{Key: k2, Name: "many"},
		},
		TTL: time.Minute,
		Load: func(_ context.Context, keys []Key) (map[Key]*testValue, error) {
			loaded = append([]Key(nil), keys...)
			return map[Key]*testValue{
				k2: &testValue{ID: 2, Name: "two"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Values[k1].Name != "one" || got.Values[k2].Name != "two" {
		t.Fatalf("unexpected values: %#v", got.Values)
	}
	if len(loaded) != 1 || loaded[0] != k2 {
		t.Fatalf("loaded keys = %#v", loaded)
	}
}

func TestGetManyRemoteMGetFailureFallsBackToLoader(t *testing.T) {
	ctx := context.Background()
	store := failingStore{
		Store:   NewMemoryStore(),
		mgetErr: errors.New("remote mget failed"),
	}
	c := newTestClient(t, store)
	key := MakeKey("many-mget-fail", 1)
	var loads atomic.Int64

	got, err := GetMany(ctx, c, GetManyRequest[*testValue]{
		Name:  "many-mget-fail",
		Items: []BatchItem{{Key: key, Name: "many-mget-fail"}},
		TTL:   time.Minute,
		Load: func(context.Context, []Key) (map[Key]*testValue, error) {
			loads.Add(1)
			return map[Key]*testValue{
				key: &testValue{ID: 1, Name: "loaded"},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Values[key].Name != "loaded" || loads.Load() != 1 {
		t.Fatalf("unexpected result: %#v loads=%d", got, loads.Load())
	}
}

func TestGetManyMissingValuesAreNegativeCached(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	k1 := MakeKey("many-missing", 1)
	k2 := MakeKey("many-missing", 2)
	var loads atomic.Int64

	req := GetManyRequest[*testValue]{
		Name:        "many-missing",
		TTL:         time.Minute,
		NegativeTTL: time.Minute,
		Items: []BatchItem{
			{Key: k1, Name: "many-missing"},
			{Key: k2, Name: "many-missing"},
		},
		Load: func(_ context.Context, keys []Key) (map[Key]*testValue, error) {
			loads.Add(1)
			return map[Key]*testValue{
				k1: &testValue{ID: 1, Name: "one"},
			}, nil
		},
	}
	got, err := GetMany(ctx, c, req)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if got.Values[k1].Name != "one" {
		t.Fatalf("missing loaded value: %#v", got.Values)
	}
	if !errors.Is(got.Errors[k2], ErrNotFound) {
		t.Fatalf("missing per-key not found: %#v", got.Errors)
	}

	_, err = GetMany(ctx, c, GetManyRequest[*testValue]{
		Name:        "many-missing",
		TTL:         time.Minute,
		NegativeTTL: time.Minute,
		Items:       []BatchItem{{Key: k2, Name: "many-missing"}},
		Load: func(context.Context, []Key) (map[Key]*testValue, error) {
			loads.Add(1)
			return nil, errors.New("loader should not run")
		},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestGetManyRejectsDuplicateKeys(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	key := MakeKey("many-duplicate", 1)
	_, err := GetMany(ctx, c, GetManyRequest[*testValue]{
		Items: []BatchItem{{Key: key}, {Key: key}},
		Load: func(context.Context, []Key) (map[Key]*testValue, error) {
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentGetManyUsesSingleflight(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	items := []BatchItem{
		{Key: MakeKey("many-singleflight", 1), Name: "many-singleflight"},
		{Key: MakeKey("many-singleflight", 2), Name: "many-singleflight"},
	}
	var loads atomic.Int64
	req := GetManyRequest[*testValue]{
		Name:  "many-singleflight",
		Items: items,
		TTL:   time.Minute,
		Load: func(_ context.Context, keys []Key) (map[Key]*testValue, error) {
			loads.Add(1)
			time.Sleep(20 * time.Millisecond)
			out := make(map[Key]*testValue, len(keys))
			for i, key := range keys {
				out[key] = &testValue{ID: i + 1}
			}
			return out, nil
		},
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := GetMany(ctx, c, req)
			if err == nil && len(result.Values) != len(items) {
				err = errors.New("missing values")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestConcurrentMissUsesSingleflight(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name: "singleflight",
		Key:  MakeKey("singleflight", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			loads.Add(1)
			time.Sleep(20 * time.Millisecond)
			return &testValue{ID: 1}, nil
		},
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Get(ctx, c, req)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestDistributedStampedeProtection(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	newClient := func(t *testing.T) *Client {
		t.Helper()
		c, err := New(Config{
			Namespace: "stampede",
			Store:     store,
			Local:     NewNoopLocal(),
			Codec:     JSONCodec{},
			Defaults: Defaults{
				TTL:         time.Minute,
				Stale:       time.Minute,
				NegativeTTL: time.Second,
				Consistency: ConsistencyFast,
			},
			Limits: Limits{
				MaxTTL:             time.Hour,
				MaxStale:           time.Hour,
				RefreshTimeout:     time.Second,
				RefreshConcurrency: 8,
				VersionCacheTTL:    time.Second,
			},
			Stampede: StampedeProtection{
				Distributed:  true,
				LockTTL:      time.Second,
				WaitTimeout:  500 * time.Millisecond,
				PollInterval: 5 * time.Millisecond,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	c1 := newClient(t)
	c2 := newClient(t)
	key := MakeKey("distributed-singleflight", 1)
	var loads atomic.Int64
	load := func(context.Context) (*testValue, error) {
		n := int(loads.Add(1))
		time.Sleep(50 * time.Millisecond)
		return &testValue{ID: n}, nil
	}
	req := GetRequest[*testValue]{
		Name: "distributed-singleflight",
		Key:  key,
		TTL:  time.Minute,
		Load: load,
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	values := make(chan int, 2)
	for _, c := range []*Client{c1, c2} {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			<-start
			got, err := Get(ctx, c, req)
			if err != nil {
				errs <- err
				return
			}
			values <- got.ID
		}(c)
	}
	close(start)
	wg.Wait()
	close(errs)
	close(values)
	for err := range errs {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
	for id := range values {
		if id != 1 {
			t.Fatalf("got value from unexpected load: %d", id)
		}
	}
}

func TestRedisStoreLockOwnership(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	pool := &redis.Pool{
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", mr.Addr())
		},
	}
	t.Cleanup(func() {
		_ = pool.Close()
		mr.Close()
	})

	store := NewRedisStore(pool.Get, "locks")
	token, ok, err := store.TryLock(ctx, "lock:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected first lock acquisition to succeed")
	}
	_, ok, err = store.TryLock(ctx, "lock:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected second lock acquisition to fail")
	}
	if err := store.Unlock(ctx, "lock:1", "wrong-token"); err != nil {
		t.Fatal(err)
	}
	_, ok, err = store.TryLock(ctx, "lock:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("wrong token released lock")
	}
	if err := store.Unlock(ctx, "lock:1", token); err != nil {
		t.Fatal(err)
	}
	_, ok, err = store.TryLock(ctx, "lock:1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected lock acquisition after owner unlock")
	}
}

func TestNegativeCache(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name:        "negative",
		Key:         MakeKey("negative", 1),
		TTL:         time.Minute,
		NegativeTTL: time.Minute,
		Load: func(context.Context) (*testValue, error) {
			loads.Add(1)
			return nil, ErrNotFound
		},
	}
	_, err := Get(ctx, c, req)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	_, err = Get(ctx, c, req)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestInvalidateKeyReloads(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64
	key := MakeKey("invalidate-key", 1)
	req := GetRequest[*testValue]{
		Name: "invalidate-key",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Invalidate(ctx, key); err != nil {
		t.Fatal(err)
	}
	second, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected reload after invalidate")
	}
}

func TestLostInvalidationEventConvergesAfterVersionCacheTTL(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	c := newTestClient(t, store)
	key := MakeKey("lost-event", 1)
	var loads atomic.Int64
	req := GetRequest[*testValue]{
		Name: "lost-event",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BumpVersions(ctx, c.keyVersionName(key)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	second, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected reload after version cache TTL without pubsub")
	}
}

func TestInvalidateTagsReloadsAllTaggedEntries(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	tag := MakeTag("user", 7)
	var loads atomic.Int64

	req := func(name string) GetRequest[*testValue] {
		return GetRequest[*testValue]{
			Name: name,
			Key:  MakeKey(name, 7),
			TTL:  time.Minute,
			Tags: []Tag{tag},
			Load: func(context.Context) (*testValue, error) {
				n := int(loads.Add(1))
				return &testValue{ID: n}, nil
			},
		}
	}
	a1, err := Get(ctx, c, req("profile"))
	if err != nil {
		t.Fatal(err)
	}
	b1, err := Get(ctx, c, req("orders"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateTags(ctx, tag); err != nil {
		t.Fatal(err)
	}
	a2, err := Get(ctx, c, req("profile"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Get(ctx, c, req("orders"))
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID == a2.ID || b1.ID == b2.ID {
		t.Fatalf("expected both tagged entries to reload")
	}
}

func TestInvalidateScopeAlias(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	scope := MakeScope("account", 9)
	key := MakeKey("scope-alias", 9)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name: "scope-alias",
		Key:  key,
		TTL:  time.Minute,
		Tags: []Tag{scope},
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	second, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected reload after scope invalidation")
	}
}

func TestInvalidateAllReloadsEveryEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := func(name string) GetRequest[*testValue] {
		return GetRequest[*testValue]{
			Name: name,
			Key:  MakeKey(name, 1),
			TTL:  time.Minute,
			Load: func(context.Context) (*testValue, error) {
				n := int(loads.Add(1))
				return &testValue{ID: n}, nil
			},
		}
	}
	a1, err := Get(ctx, c, req("all-a"))
	if err != nil {
		t.Fatal(err)
	}
	b1, err := Get(ctx, c, req("all-b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateAll(ctx); err != nil {
		t.Fatal(err)
	}
	a2, err := Get(ctx, c, req("all-a"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Get(ctx, c, req("all-b"))
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID == a2.ID || b1.ID == b2.ID {
		t.Fatalf("expected every entry to reload after InvalidateAll")
	}
}

func TestInvalidationEventsDoNotExposeLogicalNames(t *testing.T) {
	ctx := context.Background()
	store := newRecordingStore()
	c := newTestClient(t, store)

	key := MakeKey("private-user-profile", 42)
	tag := MakeTag("private-user", 42)
	if err := c.Invalidate(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateTags(ctx, tag); err != nil {
		t.Fatal(err)
	}

	var seen int
	deadline := time.After(time.Second)
	for seen < 2 {
		select {
		case event := <-store.events:
			seen++
			if event.Name == string(key) || event.Name == string(tag) {
				t.Fatalf("event exposed logical name: %#v", event)
			}
			if event.Name == "" {
				t.Fatalf("event missing version name: %#v", event)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for invalidation events")
		}
	}
}

func TestStaleWhileRevalidate(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name:  "stale-refresh",
		Key:   MakeKey("stale-refresh", 1),
		TTL:   20 * time.Millisecond,
		Stale: time.Second,
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	stale, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if stale.ID != first.ID {
		t.Fatalf("expected stale value while refresh runs")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := Get(ctx, c, req)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID > first.ID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("refresh did not update cache; loads=%d", loads.Load())
}

func TestHardExpiredEntryReloadsSynchronously(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64

	req := GetRequest[*testValue]{
		Name:  "hard-expired",
		Key:   MakeKey("hard-expired", 1),
		TTL:   20 * time.Millisecond,
		Stale: 20 * time.Millisecond,
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	second, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatalf("expected synchronous reload after hard expiry")
	}
	if loads.Load() != 2 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestStaleIfError(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var fail atomic.Bool

	req := GetRequest[*testValue]{
		Name:  "stale-if-error",
		Key:   MakeKey("stale-if-error", 1),
		TTL:   20 * time.Millisecond,
		Stale: time.Second,
		Policy: Policy{
			StaleIfError: true,
		},
		Load: func(context.Context) (*testValue, error) {
			if fail.Load() {
				return nil, errors.New("backend down")
			}
			return &testValue{ID: 1}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	time.Sleep(30 * time.Millisecond)
	got, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID {
		t.Fatalf("expected stale value")
	}
}

func TestTagsAreCanonicalized(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	var loads atomic.Int64
	a := MakeTag("a", 1)
	b := MakeTag("b", 1)
	key := MakeKey("canonical-tags", 1)

	load := func(context.Context) (*testValue, error) {
		loads.Add(1)
		return &testValue{ID: 1}, nil
	}
	_, err := Get(ctx, c, GetRequest[*testValue]{
		Name: "canonical-tags",
		Key:  key,
		TTL:  time.Minute,
		Tags: []Tag{b, a, a},
		Load: load,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Get(ctx, c, GetRequest[*testValue]{
		Name: "canonical-tags",
		Key:  key,
		TTL:  time.Minute,
		Tags: []Tag{a, b},
		Load: load,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader called %d times", loads.Load())
	}
}

func TestAlternateCodecs(t *testing.T) {
	codecs := []Codec{GobCodec{}, MsgpackCodec{}}
	for _, tc := range codecs {
		t.Run(tc.Name(), func(t *testing.T) {
			ctx := context.Background()
			c := newTestClient(t, nil)
			got, err := Get(ctx, c, GetRequest[testValue]{
				Name:  "codec-" + tc.Name(),
				Key:   MakeKey("codec", tc.Name()),
				TTL:   time.Minute,
				Codec: tc,
				Load: func(context.Context) (testValue, error) {
					return testValue{ID: 1, Name: tc.Name()}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.Name() {
				t.Fatalf("got %q", got.Name)
			}
		})
	}
}

func TestProtoCodecPointerValue(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	got, err := Get(ctx, c, GetRequest[*wrapperspb.StringValue]{
		Name:  "proto-pointer",
		Key:   MakeKey("proto-pointer", 1),
		TTL:   time.Minute,
		Codec: ProtoCodec{},
		Load: func(context.Context) (*wrapperspb.StringValue, error) {
			return wrapperspb.String("hello"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetValue() != "hello" {
		t.Fatalf("got %q", got.GetValue())
	}
}

func TestStatsObserver(t *testing.T) {
	ctx := context.Background()
	stats := NewStatsObserver()
	c, err := New(Config{
		Namespace: "stats",
		Store:     NewMemoryStore(),
		Local:     NewMemoryLocal(1 << 20),
		Codec:     JSONCodec{},
		Observer:  stats,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	req := GetRequest[*testValue]{
		Name: "stats",
		Key:  MakeKey("stats", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1}, nil
		},
	}
	if _, err := Get(ctx, c, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, c, req); err != nil {
		t.Fatal(err)
	}

	snap := stats.Snapshot()
	if snap.Counts[StatsKey{Operation: "get", Result: "load"}] != 1 {
		t.Fatalf("missing load observation: %#v", snap.Counts)
	}
	if snap.Counts[StatsKey{Operation: "get", Result: "l1_hit"}] != 1 {
		t.Fatalf("missing l1 hit observation: %#v", snap.Counts)
	}
}

func TestMetricsObserverEmitsStableMetricNames(t *testing.T) {
	ctx := context.Background()
	recorder := newRecordingMetrics()
	c, err := New(Config{
		Namespace: "metrics",
		Store:     NewMemoryStore(),
		Local:     NewMemoryLocal(1 << 20),
		Codec:     JSONCodec{},
		Observer:  MetricsObserver{Recorder: recorder},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	key := MakeKey("metrics", 1)
	req := GetRequest[*testValue]{
		Name: "metrics",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1}, nil
		},
	}
	if _, err := Get(ctx, c, req); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ctx, c, req); err != nil {
		t.Fatal(err)
	}
	if err := c.Invalidate(ctx, key); err != nil {
		t.Fatal(err)
	}

	for _, metric := range []string{
		MetricGetTotal,
		MetricL1HitTotal,
		MetricL1MissTotal,
		MetricL2MissTotal,
		MetricLoadTotal,
		MetricInvalidateTotal,
	} {
		if recorder.counter(metric) == 0 {
			t.Fatalf("missing counter %s", metric)
		}
	}
	if recorder.histogram(MetricLoadDurationSeconds) == 0 {
		t.Fatalf("missing load duration histogram")
	}
}

func TestOpenTelemetryTracerConfig(t *testing.T) {
	ctx := context.Background()
	c, err := New(Config{
		Namespace: "trace",
		Store:     NewMemoryStore(),
		Local:     NewMemoryLocal(1 << 20),
		Codec:     JSONCodec{},
		Tracer:    noop.NewTracerProvider().Tracer("tiercache-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = Get(ctx, c, GetRequest[*testValue]{
		Name: "trace",
		Key:  MakeKey("trace", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDisabledClientBypassesCache(t *testing.T) {
	ctx := context.Background()
	c, err := New(Config{
		Namespace: "disabled",
		Store:     NewMemoryStore(),
		Local:     NewMemoryLocal(1 << 20),
		Codec:     JSONCodec{},
		Disabled:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	key := MakeKey("disabled", 1)
	var loads atomic.Int64
	req := GetRequest[*testValue]{
		Name: "disabled",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			n := int(loads.Add(1))
			return &testValue{ID: n}, nil
		},
	}
	first, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Get(ctx, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || loads.Load() != 2 {
		t.Fatalf("expected disabled client to bypass cache")
	}

	var batchLoads atomic.Int64
	result, err := GetMany(ctx, c, GetManyRequest[*testValue]{
		Name:  "disabled-many",
		Items: []BatchItem{{Key: key, Name: "disabled-many"}},
		TTL:   time.Minute,
		Load: func(context.Context, []Key) (map[Key]*testValue, error) {
			n := int(batchLoads.Add(1))
			return map[Key]*testValue{key: &testValue{ID: n}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Values[key].ID != 1 {
		t.Fatalf("got %#v", result.Values[key])
	}
	result, err = GetMany(ctx, c, GetManyRequest[*testValue]{
		Name:  "disabled-many",
		Items: []BatchItem{{Key: key, Name: "disabled-many"}},
		TTL:   time.Minute,
		Load: func(context.Context, []Key) (map[Key]*testValue, error) {
			n := int(batchLoads.Add(1))
			return map[Key]*testValue{key: &testValue{ID: n}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Values[key].ID != 2 || batchLoads.Load() != 2 {
		t.Fatalf("expected disabled GetMany to bypass cache")
	}
	if err := c.Invalidate(ctx, key); err != nil {
		t.Fatal(err)
	}
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, nil)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Get(ctx, c, GetRequest[*testValue]{
		Key: MakeKey("closed", 1),
		TTL: time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{}, nil
		},
	})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestMakeKeyIsDeterministicAndDelimiterSafe(t *testing.T) {
	a := MakeKey("user", "1:2", 3)
	b := MakeKey("user", "1", "2:3")
	c := MakeKey("user", "1:2", 3)
	if a == b {
		t.Fatalf("different arguments produced same key: %q", a)
	}
	if a != c {
		t.Fatalf("same arguments produced different keys: %q vs %q", a, c)
	}
}

func FuzzMakeKey(f *testing.F) {
	f.Add("user", "1", "2")
	f.Add("user:1", "2", "3")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, name, a, b string) {
		k1 := MakeKey(name, a, b)
		k2 := MakeKey(name, a, b)
		if k1 != k2 {
			t.Fatalf("non-deterministic key: %q != %q", k1, k2)
		}
		if a != b && MakeKey(name, a, b) == MakeKey(name, b, a) {
			t.Fatalf("argument order collision for %q, %q", a, b)
		}
	})
}
