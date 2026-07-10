package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
)

type testValue struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type failingStore struct {
	Store
	getErr      error
	versionsErr error
}

type countingStore struct {
	Store
	versionCalls atomic.Int32
}

func (s *countingStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	s.versionCalls.Add(1)
	return s.Store.GetVersions(ctx, names...)
}

func (s failingStore) Get(context.Context, string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return nil, ErrNotFound
}

func (s failingStore) GetVersions(ctx context.Context, names ...string) (map[string]int64, error) {
	if s.versionsErr != nil {
		return nil, s.versionsErr
	}
	return s.Store.GetVersions(ctx, names...)
}

func newTestClient(t *testing.T, store Store, configure ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Namespace: "test",
		Store:     store,
		Local:     NewLocalCache(8 << 20),
		Limits: Limits{
			MaxTTL:             time.Minute,
			MaxStale:           time.Second,
			LoadTimeout:        time.Second,
			RefreshTimeout:     time.Second,
			RefreshConcurrency: 8,
			VersionCacheTTL:    10 * time.Millisecond,
		},
	}
	for _, fn := range configure {
		fn(&cfg)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return client
}

func TestGetReadThroughAndL1Hit(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("user.profile", 7)
	var loads atomic.Int32
	load := func(context.Context) (*testValue, error) {
		loads.Add(1)
		return &testValue{ID: 7, Name: "Ada"}, nil
	}
	first, err := Get(context.Background(), c, key, time.Second, load)
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "mutated"
	second, err := Get(context.Background(), c, key, time.Second, load)
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != "Ada" || loads.Load() != 1 {
		t.Fatalf("got %+v with %d loads", second, loads.Load())
	}
}

func TestConcurrentGetUsesOneLoader(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("hot", 1)
	var loads atomic.Int32
	load := func(context.Context) (int, error) {
		loads.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 42, nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := Get(context.Background(), c, key, time.Second, load)
			if err != nil || value != 42 {
				errs <- errors.New("unexpected result")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times", loads.Load())
	}
}

func TestDifferentTypesNeverShareFlightOrStorage(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("same.logical.key", 1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var stringValue string
	var intValue int
	var stringErr, intErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		stringValue, stringErr = Get(context.Background(), c, key, time.Second, func(context.Context) (string, error) {
			<-start
			return "value", nil
		})
	}()
	go func() {
		defer wg.Done()
		intValue, intErr = Get(context.Background(), c, key, time.Second, func(context.Context) (int, error) {
			<-start
			return 9, nil
		})
	}()
	close(start)
	wg.Wait()
	if stringErr != nil || intErr != nil || stringValue != "value" || intValue != 9 {
		t.Fatalf("string=(%q,%v), int=(%d,%v)", stringValue, stringErr, intValue, intErr)
	}
}

func TestCanceledWaiterDoesNotCancelSharedLoad(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("cancel", 1)
	started := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (int, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 11, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := Get(ctx, c, key, time.Second, load)
		first <- err
	}()
	<-started
	second := make(chan error, 1)
	go func() {
		value, err := Get(context.Background(), c, key, time.Second, load)
		if err == nil && value != 11 {
			err = errors.New("wrong value")
		}
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first error = %v", err)
	}
	close(release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestLoaderPanicBecomesErrorAndFlightRecovers(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("panic", 1)
	_, err := Get(context.Background(), c, key, time.Second, func(context.Context) (int, error) {
		panic("boom")
	})
	var panicErr LoaderPanicError
	if !errors.As(err, &panicErr) || len(panicErr.Stack) == 0 {
		t.Fatalf("error = %v", err)
	}
	value, err := Get(context.Background(), c, key, time.Second, func(context.Context) (int, error) {
		return 5, nil
	})
	if err != nil || value != 5 {
		t.Fatalf("value=%d err=%v", value, err)
	}
}

func TestNegativeCaching(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	var loads atomic.Int32
	load := func(context.Context) (*testValue, error) {
		loads.Add(1)
		return nil, ErrNotFound
	}
	for range 2 {
		_, err := Get(context.Background(), c, MakeKey("missing", 1), time.Second, load, WithNegativeTTL(time.Second))
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v", err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times", loads.Load())
	}
}

func TestFailureModes(t *testing.T) {
	infrastructureErr := errors.New("cache unavailable")
	open := newTestClient(t, failingStore{Store: NewMemoryStore(), versionsErr: infrastructureErr})
	value, err := Get(context.Background(), open, MakeKey("open"), time.Second, func(context.Context) (int, error) {
		return 3, nil
	})
	if err != nil || value != 3 {
		t.Fatalf("fail-open value=%d err=%v", value, err)
	}

	closed := newTestClient(t, failingStore{Store: NewMemoryStore(), versionsErr: infrastructureErr}, func(cfg *Config) {
		cfg.FailureMode = FailClosed
	})
	_, err = Get(context.Background(), closed, MakeKey("closed-mode"), time.Second, func(context.Context) (int, error) {
		return 3, nil
	})
	if !errors.Is(err, infrastructureErr) {
		t.Fatalf("fail-closed error=%v", err)
	}
}

func TestObserverPanicCannotBreakReads(t *testing.T) {
	c := newTestClient(t, NewMemoryStore(), func(cfg *Config) {
		cfg.Observer = ObserverFunc(func(Observation) { panic("metrics failure") })
	})
	value, err := Get(context.Background(), c, MakeKey("observer"), time.Second, func(context.Context) (int, error) {
		return 7, nil
	})
	if err != nil || value != 7 {
		t.Fatalf("value=%d err=%v", value, err)
	}
}

func TestInvalidateAndTags(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	ctx := context.Background()
	key := MakeKey("profile", 1)
	tag := MakeTag("user", 1)
	var loads atomic.Int32
	load := func(context.Context) (int, error) { return int(loads.Add(1)), nil }
	value, err := Get(ctx, c, key, time.Second, load, WithTags(tag))
	if err != nil || value != 1 {
		t.Fatalf("first = %d, %v", value, err)
	}
	if err := c.Invalidate(ctx, key); err != nil {
		t.Fatal(err)
	}
	value, err = Get(ctx, c, key, time.Second, load, WithTags(tag))
	if err != nil || value != 2 {
		t.Fatalf("after key invalidation = %d, %v", value, err)
	}
	if err := c.InvalidateTags(ctx, tag); err != nil {
		t.Fatal(err)
	}
	value, err = Get(ctx, c, key, time.Second, load, WithTags(tag))
	if err != nil || value != 3 {
		t.Fatalf("after tag invalidation = %d, %v", value, err)
	}
}

func TestVersionCacheNeverMovesBackward(t *testing.T) {
	versions := newVersionCache(time.Second, 64)
	now := time.Now()
	versions.setMax("key", 7, now)
	versions.setMax("key", 3, now.Add(time.Millisecond))
	value, ok := versions.get("key", now.Add(2*time.Millisecond))
	if !ok || value != 7 {
		t.Fatalf("version = %d, %v", value, ok)
	}
}

func TestMetadataCachesAreBounded(t *testing.T) {
	versions := newVersionCache(time.Minute, 64)
	now := time.Now()
	for i := 0; i < 10_000; i++ {
		versions.setMax(MakeKey("version", i).String(), int64(i), now)
	}
	versionCount := 0
	for i := range versions.shards {
		shard := &versions.shards[i]
		shard.mu.Lock()
		versionCount += len(shard.entries)
		shard.mu.Unlock()
	}
	if versionCount > 64 {
		t.Fatalf("version cache contains %d entries", versionCount)
	}

	routes := newRouteCache(64)
	c := &Client{namespace: "bounded", routes: routes}
	for i := 0; i < 10_000; i++ {
		c.routeFor(MakeKey("route", i), "int", "", JSONCodec{}, nil, time.Second, 0, 0, 0)
	}
	routeCount := 0
	for i := range routes.shards {
		shard := &routes.shards[i]
		shard.mu.Lock()
		routeCount += len(shard.entries)
		shard.mu.Unlock()
	}
	if routeCount > 64 {
		t.Fatalf("route cache contains %d entries", routeCount)
	}
}

func TestStaleWhileRevalidate(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("stale", 1)
	var loads atomic.Int32
	load := func(context.Context) (int, error) { return int(loads.Add(1)), nil }
	value, err := Get(context.Background(), c, key, 30*time.Millisecond, load, WithStaleWhileRevalidate(time.Second))
	if err != nil || value != 1 {
		t.Fatalf("first = %d, %v", value, err)
	}
	time.Sleep(35 * time.Millisecond)
	value, err = Get(context.Background(), c, key, 30*time.Millisecond, load, WithStaleWhileRevalidate(time.Second))
	if err != nil || value != 1 {
		t.Fatalf("stale = %d, %v", value, err)
	}
	deadline := time.Now().Add(time.Second)
	for loads.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if loads.Load() != 2 {
		t.Fatalf("refresh loads = %d", loads.Load())
	}
}

func TestStaleIfError(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	key := MakeKey("stale-error", 1)
	var loads atomic.Int32
	load := func(context.Context) (int, error) {
		if loads.Add(1) == 1 {
			return 8, nil
		}
		return 0, errors.New("backend down")
	}
	if _, err := Get(context.Background(), c, key, 30*time.Millisecond, load, WithStaleIfError(time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)
	value, err := Get(context.Background(), c, key, 30*time.Millisecond, load, WithStaleIfError(time.Second))
	if err != nil || value != 8 || loads.Load() != 2 {
		t.Fatalf("value=%d err=%v loads=%d", value, err, loads.Load())
	}
}

func TestGetManyLoadsOnlyMisses(t *testing.T) {
	c := newTestClient(t, NewMemoryStore())
	ctx := context.Background()
	one := MakeKey("item", 1)
	two := MakeKey("item", 2)
	if _, err := Get(ctx, c, one, time.Second, func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	var loaded []Key
	result, err := GetMany(ctx, c, []Item[int]{{ID: 1, Key: one}, {ID: 2, Key: two}}, time.Second, func(_ context.Context, ids []int) (map[int]int, error) {
		for _, id := range ids {
			loaded = append(loaded, MakeKey("item", id))
		}
		return map[int]int{2: 2}, nil
	})
	if err != nil || result.Values[1] != 1 || result.Values[2] != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(loaded) != 1 || loaded[0] != two {
		t.Fatalf("loaded keys = %v", loaded)
	}
}

func TestGetManyReadsVersionsOnceAndRejectsDuplicateIDs(t *testing.T) {
	store := &countingStore{Store: NewMemoryStore()}
	c := newTestClient(t, store)
	items := make([]Item[int], 16)
	for i := range items {
		items[i] = Item[int]{ID: i, Key: MakeKey("batch", i), Tags: []Tag{MakeTag("group", i%2)}}
	}
	result, err := GetMany(context.Background(), c, items, time.Second, func(_ context.Context, ids []int) (map[int]int, error) {
		values := make(map[int]int, len(ids))
		for _, id := range ids {
			values[id] = id
		}
		return values, nil
	})
	if err != nil || len(result.Values) != len(items) {
		t.Fatalf("result=%v err=%v", result.Values, err)
	}
	if store.versionCalls.Load() != 1 {
		t.Fatalf("version reads = %d", store.versionCalls.Load())
	}
	_, err = GetMany(context.Background(), c, []Item[int]{
		{ID: 1, Key: MakeKey("duplicate", 1)},
		{ID: 1, Key: MakeKey("duplicate", 2)},
	}, time.Second, func(context.Context, []int) (map[int]int, error) { return nil, nil })
	if !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestDisabledClientNeedsNoStore(t *testing.T) {
	c, err := New(Config{Namespace: "disabled", Disabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	value, err := Get(context.Background(), c, MakeKey("x"), time.Second, func(context.Context) (int, error) { return 4, nil })
	if err != nil || value != 4 {
		t.Fatalf("value=%d err=%v", value, err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c, err := New(Config{Namespace: "close", Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Get(context.Background(), c, MakeKey("closed"), time.Second, func(context.Context) (int, error) { return 1, nil })
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("error = %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := New(Config{Store: NewMemoryStore()}); !errors.Is(err, ErrInvalidNamespace) {
		t.Fatalf("namespace error = %v", err)
	}
	if _, err := New(Config{Namespace: "x"}); !errors.Is(err, ErrNoStore) {
		t.Fatalf("store error = %v", err)
	}
	if _, err := New(Config{
		Namespace: "x",
		Store:     NewMemoryStore(),
		Stampede:  StampedeProtection{Distributed: true, PollInterval: -time.Second},
	}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("stampede error = %v", err)
	}
}

func TestKeyBuilder(t *testing.T) {
	first := MakeKey("method", "a:b", 1)
	second := MakeKey("method", "a", "b:1")
	if first == second || first != MakeKey("method", "a:b", 1) {
		t.Fatalf("keys are not deterministic and delimiter safe")
	}
	if _, err := BuildKey("bad", func() {}); err == nil {
		t.Fatal("expected unsupported argument error")
	}
}

func TestRedisStoreRoundTripAndVersions(t *testing.T) {
	backend := &fakeRedisBackend{data: make(map[string][]byte), versions: make(map[string]int64)}
	store, err := NewRedisStore(func() redis.Conn { return fakeRedisConn{backend: backend} })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Set(ctx, "key", []byte("value"), time.Second); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(ctx, "key")
	if err != nil || string(value) != "value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	versions, err := store.BumpVersions(ctx, "a", "b")
	if err != nil || versions["a"] != 1 || versions["b"] != 1 {
		t.Fatalf("versions=%v err=%v", versions, err)
	}
}

type fakeRedisBackend struct {
	mu       sync.Mutex
	data     map[string][]byte
	versions map[string]int64
}

type fakeRedisConn struct{ backend *fakeRedisBackend }

func (fakeRedisConn) Close() error                   { return nil }
func (fakeRedisConn) Err() error                     { return nil }
func (fakeRedisConn) Do(string, ...any) (any, error) { panic("Do must not be called") }
func (fakeRedisConn) Send(string, ...any) error      { return nil }
func (fakeRedisConn) Flush() error                   { return nil }
func (fakeRedisConn) Receive() (any, error)          { return nil, nil }
func (fakeRedisConn) ReceiveContext(ctx context.Context) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (c fakeRedisConn) DoContext(ctx context.Context, command string, args ...any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.backend.mu.Lock()
	defer c.backend.mu.Unlock()
	switch command {
	case "SET":
		key := args[0].(string)
		value := args[1].([]byte)
		c.backend.data[key] = append([]byte(nil), value...)
		return "OK", nil
	case "GET":
		value, ok := c.backend.data[args[0].(string)]
		if !ok {
			return nil, redis.ErrNil
		}
		return append([]byte(nil), value...), nil
	case "EVAL":
		keyCount := args[1].(int)
		values := make([]any, keyCount)
		for i := 0; i < keyCount; i++ {
			name := args[i+2].(string)
			c.backend.versions[name]++
			values[i] = c.backend.versions[name]
		}
		return values, nil
	default:
		return nil, errors.New("unsupported fake Redis command: " + command)
	}
}

type contextConn struct{}

func (contextConn) Close() error                   { return nil }
func (contextConn) Err() error                     { return nil }
func (contextConn) Do(string, ...any) (any, error) { panic("Do must not be called") }
func (contextConn) Send(string, ...any) error      { return nil }
func (contextConn) Flush() error                   { return nil }
func (contextConn) Receive() (any, error)          { return nil, nil }
func (contextConn) ReceiveContext(ctx context.Context) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (contextConn) DoContext(ctx context.Context, _ string, _ ...any) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRedisStoreHonorsContext(t *testing.T) {
	store, err := NewRedisStore(func() redis.Conn { return contextConn{} })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = store.Get(ctx, "key")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzBuildKey(f *testing.F) {
	f.Add("method", "argument")
	f.Add("a:b", "x|y#z")
	f.Fuzz(func(t *testing.T, name, argument string) {
		if name == "" || len(name) > 1000 {
			return
		}
		first, firstErr := BuildKey(name, argument)
		second, secondErr := BuildKey(name, argument)
		if (firstErr == nil) != (secondErr == nil) || first != second {
			t.Fatalf("results differ: %q/%v, %q/%v", first, firstErr, second, secondErr)
		}
	})
}

func FuzzDecodeRecord(f *testing.F) {
	f.Add(encodeRecord(record{softExpireAt: 1, hardExpireAt: 2, payload: []byte("x")}))
	f.Add([]byte("not-a-record"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeRecord(data)
	})
}
