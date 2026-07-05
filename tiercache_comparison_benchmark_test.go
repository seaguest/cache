package cache_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gomodule/redigo/redis"
	oldcache "github.com/seaguest/cache"
	"github.com/seaguest/cache/tiercache"
)

type benchValue struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (v *benchValue) DeepCopy() interface{} {
	c := *v
	return &c
}

func newBenchmarkRedis(b testing.TB) (*miniredis.Miniredis, *redis.Pool) {
	b.Helper()
	mr := miniredis.RunT(b)
	pool := &redis.Pool{
		MaxIdle:     64,
		MaxActive:   256,
		Wait:        true,
		IdleTimeout: time.Minute,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", mr.Addr())
		},
	}
	b.Cleanup(func() {
		_ = pool.Close()
		mr.Close()
	})
	return mr, pool
}

func newOldBenchmarkCache(b testing.TB) (*oldcache.Testing, oldcache.Cache) {
	b.Helper()
	_, pool := newBenchmarkRedis(b)
	tester, c := oldcache.NewForTesting(
		oldcache.GetConn(pool.Get),
		oldcache.Separator("#"),
		oldcache.GetPolicy(oldcache.GetPolicyReturnExpired),
		oldcache.OnError(func(context.Context, error) {}),
	)
	return tester, c
}

func newTierBenchmarkClient(b testing.TB) *tiercache.Client {
	return newTierBenchmarkClientWithLocal(b, nil)
}

func newTierBenchmarkClientWithLocal(b testing.TB, local tiercache.LocalCache) *tiercache.Client {
	b.Helper()
	_, pool := newBenchmarkRedis(b)
	c, err := tiercache.New(tiercache.Config{
		Namespace: "bench",
		Store:     tiercache.NewRedisStore(pool.Get, "bench"),
		Local:     local,
		Codec:     tiercache.JSONCodec{},
		Defaults: tiercache.Defaults{
			TTL:         time.Minute,
			Stale:       time.Minute,
			NegativeTTL: time.Second,
			Policy: tiercache.Policy{
				StaleWhileRevalidate: true,
				StaleIfError:         true,
			},
			Consistency: tiercache.ConsistencyFast,
		},
		Limits: tiercache.Limits{
			MaxTTL:             time.Hour,
			MaxStale:           time.Hour,
			RefreshTimeout:     time.Second,
			RefreshConcurrency: 32,
			VersionCacheTTL:    time.Second,
		},
		FailOpen: true,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	return c
}

func BenchmarkComparison_Tiercache_L1Hit(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClient(b)
	key := tiercache.MakeKey("bench.l1", 1)
	req := tiercache.GetRequest[*benchValue]{
		Name: "bench.l1",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*benchValue, error) {
			return &benchValue{ID: 1, Name: "value"}, nil
		},
	}
	if _, err := tiercache.Get(ctx, c, req); err != nil {
		b.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tiercache.Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_OldCache_L1Hit(b *testing.B) {
	ctx := context.Background()
	_, c := newOldBenchmarkCache(b)
	key := "bench_l1#1"
	load := func() (any, error) {
		return &benchValue{ID: 1, Name: "value"}, nil
	}
	var out benchValue
	if err := c.GetObject(ctx, key, &out, time.Minute, load); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out benchValue
		if err := c.GetObject(ctx, key, &out, time.Minute, load); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_Tiercache_L2Hit(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClientWithLocal(b, tiercache.NewNoopLocal())
	key := tiercache.MakeKey("bench.l2", 1)
	req := tiercache.GetRequest[*benchValue]{
		Name: "bench.l2",
		Key:  key,
		TTL:  time.Minute,
		Load: func(context.Context) (*benchValue, error) {
			return &benchValue{ID: 1, Name: "value"}, nil
		},
	}
	if _, err := tiercache.Get(ctx, c, req); err != nil {
		b.Fatal(err)
	}

	req.Load = func(context.Context) (*benchValue, error) {
		b.Fatal("loader called on L2 hit")
		return nil, nil
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tiercache.Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_OldCache_L2Hit(b *testing.B) {
	ctx := context.Background()
	tester, c := newOldBenchmarkCache(b)
	key := "bench_l2#1"
	load := func() (any, error) {
		return &benchValue{ID: 1, Name: "value"}, nil
	}
	var out benchValue
	if err := c.GetObject(ctx, key, &out, time.Minute, load); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tester.DeleteFromMem(key)
		var out benchValue
		if err := c.GetObject(ctx, key, &out, time.Minute, func() (any, error) {
			b.Fatal("loader called on L2 hit")
			return nil, nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_Tiercache_MissLoad(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClient(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := tiercache.GetRequest[*benchValue]{
			Name: "bench.miss",
			Key:  tiercache.MakeKey("bench.miss", i),
			TTL:  time.Minute,
			Load: func(context.Context) (*benchValue, error) {
				return &benchValue{ID: i, Name: "value"}, nil
			},
		}
		if _, err := tiercache.Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_OldCache_MissLoad(b *testing.B) {
	ctx := context.Background()
	_, c := newOldBenchmarkCache(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := "bench_miss#" + strconv.Itoa(i)
		var out benchValue
		if err := c.GetObject(ctx, key, &out, time.Minute, func() (any, error) {
			return &benchValue{ID: i, Name: "value"}, nil
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_Tiercache_ParallelSameKey(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClient(b)
	req := tiercache.GetRequest[*benchValue]{
		Name: "bench.parallel",
		Key:  tiercache.MakeKey("bench.parallel", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*benchValue, error) {
			return &benchValue{ID: 1, Name: "value"}, nil
		},
	}
	if _, err := tiercache.Get(ctx, c, req); err != nil {
		b.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := tiercache.Get(ctx, c, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkComparison_OldCache_ParallelSameKey(b *testing.B) {
	ctx := context.Background()
	_, c := newOldBenchmarkCache(b)
	key := "bench_parallel#1"
	load := func() (any, error) {
		return &benchValue{ID: 1, Name: "value"}, nil
	}
	var out benchValue
	if err := c.GetObject(ctx, key, &out, time.Minute, load); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			var out benchValue
			if err := c.GetObject(ctx, key, &out, time.Minute, load); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkComparison_Tiercache_GetMany_L2Hit_16(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClientWithLocal(b, tiercache.NewNoopLocal())
	items := make([]tiercache.BatchItem, 16)
	seed := make(map[tiercache.Key]*benchValue, len(items))
	for i := range items {
		key := tiercache.MakeKey("bench.many.l2", i)
		items[i] = tiercache.BatchItem{Key: key, Name: "bench.many.l2"}
		seed[key] = &benchValue{ID: i, Name: "value"}
	}
	req := tiercache.GetManyRequest[*benchValue]{
		Name:  "bench.many.l2",
		Items: items,
		TTL:   time.Minute,
		Load: func(context.Context, []tiercache.Key) (map[tiercache.Key]*benchValue, error) {
			return seed, nil
		},
	}
	if _, err := tiercache.GetMany(ctx, c, req); err != nil {
		b.Fatal(err)
	}
	req.Load = func(context.Context, []tiercache.Key) (map[tiercache.Key]*benchValue, error) {
		b.Fatal("loader called on GetMany L2 hit")
		return nil, nil
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tiercache.GetMany(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_OldCache_GetMany_L2Hit_16(b *testing.B) {
	ctx := context.Background()
	tester, c := newOldBenchmarkCache(b)
	keys := make([]string, 16)
	for i := range keys {
		keys[i] = "bench_many_l2#" + strconv.Itoa(i)
		var out benchValue
		if err := c.GetObject(ctx, keys[i], &out, time.Minute, func() (any, error) {
			return &benchValue{ID: i, Name: "value"}, nil
		}); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, key := range keys {
			tester.DeleteFromMem(key)
			var out benchValue
			if err := c.GetObject(ctx, key, &out, time.Minute, func() (any, error) {
				b.Fatal("loader called on old batch L2 hit")
				return nil, nil
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkComparison_Tiercache_GetMany_MissLoad_16(b *testing.B) {
	ctx := context.Background()
	c := newTierBenchmarkClient(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items := make([]tiercache.BatchItem, 16)
		values := make(map[tiercache.Key]*benchValue, len(items))
		for j := range items {
			key := tiercache.MakeKey("bench.many.miss", i, j)
			items[j] = tiercache.BatchItem{Key: key, Name: "bench.many.miss"}
			values[key] = &benchValue{ID: j, Name: "value"}
		}
		if _, err := tiercache.GetMany(ctx, c, tiercache.GetManyRequest[*benchValue]{
			Name:  "bench.many.miss",
			Items: items,
			TTL:   time.Minute,
			Load: func(context.Context, []tiercache.Key) (map[tiercache.Key]*benchValue, error) {
				return values, nil
			},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkComparison_OldCache_GetMany_MissLoad_16(b *testing.B) {
	ctx := context.Background()
	_, c := newOldBenchmarkCache(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 16; j++ {
			key := "bench_many_miss#" + strconv.Itoa(i) + "#" + strconv.Itoa(j)
			var out benchValue
			if err := c.GetObject(ctx, key, &out, time.Minute, func() (any, error) {
				return &benchValue{ID: j, Name: "value"}, nil
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
}
