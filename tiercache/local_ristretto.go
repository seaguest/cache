package tiercache

import (
	"time"

	ristretto "github.com/dgraph-io/ristretto/v2"
)

type RistrettoConfig struct {
	MaxCost     int64
	NumCounters int64
	BufferItems int64
	Metrics     bool
}

type ristrettoLocal struct {
	cache *ristretto.Cache[string, []byte]
}

// NewRistrettoLocal returns the default bounded local cache adapter.
func NewRistrettoLocal(cfg RistrettoConfig) (LocalCache, error) {
	if cfg.MaxCost <= 0 {
		cfg.MaxCost = 64 << 20
	}
	if cfg.NumCounters <= 0 {
		cfg.NumCounters = 1_000_000
	}
	if cfg.BufferItems <= 0 {
		cfg.BufferItems = 64
	}
	c, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters: cfg.NumCounters,
		MaxCost:     cfg.MaxCost,
		BufferItems: cfg.BufferItems,
		Metrics:     cfg.Metrics,
	})
	if err != nil {
		return nil, err
	}
	return &ristrettoLocal{cache: c}, nil
}

func (l *ristrettoLocal) Get(key string) ([]byte, bool) {
	v, ok := l.cache.Get(key)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

func (l *ristrettoLocal) Set(key string, value []byte, cost int64, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	if cost <= 0 {
		cost = int64(len(value))
	}
	return l.cache.SetWithTTL(key, append([]byte(nil), value...), cost, ttl)
}

func (l *ristrettoLocal) Delete(keys ...string) {
	for _, key := range keys {
		l.cache.Del(key)
	}
}

func (l *ristrettoLocal) Clear() { l.cache.Clear() }

func (l *ristrettoLocal) Close() error {
	l.cache.Close()
	return nil
}
