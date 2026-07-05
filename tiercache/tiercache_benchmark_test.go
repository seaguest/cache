package tiercache

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func newBenchmarkClient(b *testing.B, local LocalCache) *Client {
	b.Helper()
	if local == nil {
		local = NewMemoryLocal(64 << 20)
	}
	c, err := New(Config{
		Namespace: "bench",
		Store:     NewMemoryStore(),
		Local:     local,
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

func BenchmarkGet_L1Hit_JSON(b *testing.B) {
	benchmarkL1Hit(b, JSONCodec{})
}

func BenchmarkGet_L1Hit_Msgpack(b *testing.B) {
	benchmarkL1Hit(b, MsgpackCodec{})
}

func benchmarkL1Hit(b *testing.B, codec Codec) {
	ctx := context.Background()
	c := newBenchmarkClient(b, nil)
	req := GetRequest[*testValue]{
		Name:  "bench.l1",
		Key:   MakeKey("bench.l1", codec.Name()),
		TTL:   time.Minute,
		Codec: codec,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "value"}, nil
		},
	}
	if _, err := Get(ctx, c, req); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet_L2Hit(b *testing.B) {
	ctx := context.Background()
	c := newBenchmarkClient(b, NewNoopLocal())
	req := GetRequest[*testValue]{
		Name: "bench.l2",
		Key:  MakeKey("bench.l2", 1),
		TTL:  time.Minute,
		Load: func(context.Context) (*testValue, error) {
			return &testValue{ID: 1, Name: "value"}, nil
		},
	}
	if _, err := Get(ctx, c, req); err != nil {
		b.Fatal(err)
	}
	req.Load = func(context.Context) (*testValue, error) {
		b.Fatal("loader called on L2 hit")
		return nil, nil
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet_Miss_Load(b *testing.B) {
	ctx := context.Background()
	c := newBenchmarkClient(b, nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := GetRequest[*testValue]{
			Name: "bench.miss",
			Key:  MakeKey("bench.miss", i),
			TTL:  time.Minute,
			Load: func(context.Context) (*testValue, error) {
				return &testValue{ID: i, Name: "value"}, nil
			},
		}
		if _, err := Get(ctx, c, req); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGet_ParallelDifferentKeys(b *testing.B) {
	ctx := context.Background()
	c := newBenchmarkClient(b, nil)
	var seq atomic.Int64

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			id := seq.Add(1)
			req := GetRequest[*testValue]{
				Name: "bench.parallel.different",
				Key:  MakeKey("bench.parallel.different", id),
				TTL:  time.Minute,
				Load: func(context.Context) (*testValue, error) {
					return &testValue{ID: int(id), Name: "value"}, nil
				},
			}
			if _, err := Get(ctx, c, req); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkInvalidateScope(b *testing.B) {
	ctx := context.Background()
	c := newBenchmarkClient(b, nil)
	scope := MakeScope("bench.scope", 1)

	for i := 0; i < 1024; i++ {
		key := MakeKey("bench.scope.item", i)
		if _, err := Get(ctx, c, GetRequest[*testValue]{
			Name: "bench.scope.item",
			Key:  key,
			Tags: []Tag{scope},
			TTL:  time.Minute,
			Load: func(context.Context) (*testValue, error) {
				return &testValue{ID: i}, nil
			},
		}); err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.InvalidateScope(ctx, scope); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKeyBuilder(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MakeKey("bench.key", i, "status", strconv.Itoa(i%10))
	}
}
