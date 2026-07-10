package cache

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func benchmarkClient(b *testing.B, local LocalCache) *Client {
	b.Helper()
	c, err := New(Config{
		Namespace: "benchmark",
		Store:     NewMemoryStore(),
		Local:     local,
		Limits: Limits{
			VersionCacheTTL: time.Hour,
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	return c
}

func BenchmarkGetL1Hit(b *testing.B) {
	c := benchmarkClient(b, NewLocalCache(64<<20))
	key := MakeKey("profile", 1)
	load := func(context.Context) (*testValue, error) { return &testValue{ID: 1, Name: "Ada"}, nil }
	if _, err := Get(context.Background(), c, key, time.Minute, load); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Get(context.Background(), c, key, time.Minute, load); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetL2Hit(b *testing.B) {
	c := benchmarkClient(b, NewNoopLocal())
	key := MakeKey("profile", 1)
	load := func(context.Context) (*testValue, error) { return &testValue{ID: 1, Name: "Ada"}, nil }
	if _, err := Get(context.Background(), c, key, time.Minute, load); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Get(context.Background(), c, key, time.Minute, load); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetParallelDifferentKeys(b *testing.B) {
	c := benchmarkClient(b, NewLocalCache(64<<20))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := MakeKey("profile", strconv.Itoa(i%1024))
			_, err := Get(context.Background(), c, key, time.Minute, func(context.Context) (int, error) { return i, nil })
			if err != nil {
				b.Error(err)
			}
			i++
		}
	})
}

func BenchmarkGetL1HitWithTag(b *testing.B) {
	c := benchmarkClient(b, NewLocalCache(64<<20))
	key := MakeKey("profile", 1)
	tag := MakeTag("user", 1)
	load := func(context.Context) (*testValue, error) { return &testValue{ID: 1, Name: "Ada"}, nil }
	if _, err := Get(context.Background(), c, key, time.Minute, load, WithTags(tag)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Get(context.Background(), c, key, time.Minute, load, WithTags(tag)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetManyL2Hit16(b *testing.B) {
	c := benchmarkClient(b, NewNoopLocal())
	items := make([]Item[int], 16)
	for i := range items {
		items[i] = Item[int]{ID: i, Key: MakeKey("profile", i)}
	}
	load := func(_ context.Context, ids []int) (map[int]*testValue, error) {
		values := make(map[int]*testValue, len(ids))
		for _, id := range ids {
			values[id] = &testValue{ID: id, Name: "Ada"}
		}
		return values, nil
	}
	if _, err := GetMany(context.Background(), c, items, time.Minute, load); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := GetMany(context.Background(), c, items, time.Minute, load); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMakeKey(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MakeKey("UserService.Profile", int64(i), "active")
	}
}
