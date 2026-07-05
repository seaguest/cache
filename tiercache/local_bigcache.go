package tiercache

import (
	"context"
	"errors"
	"time"

	bigcache "github.com/allegro/bigcache/v3"
)

type BigCacheConfig struct {
	MaxBytes int64
	TTL      time.Duration
}

type bigCacheLocal struct {
	cache *bigcache.BigCache
}

// NewBigCacheLocal returns a GC-friendly bytes local cache adapter.
func NewBigCacheLocal(cfg BigCacheConfig) (LocalCache, error) {
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	bcCfg := bigcache.DefaultConfig(cfg.TTL)
	if cfg.MaxBytes > 0 {
		bcCfg.HardMaxCacheSize = int(cfg.MaxBytes / (1 << 20))
		if bcCfg.HardMaxCacheSize == 0 {
			bcCfg.HardMaxCacheSize = 1
		}
	}
	c, err := bigcache.New(context.Background(), bcCfg)
	if err != nil {
		return nil, err
	}
	return &bigCacheLocal{cache: c}, nil
}

func (l *bigCacheLocal) Get(key string) ([]byte, bool) {
	v, err := l.cache.Get(key)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

func (l *bigCacheLocal) Set(key string, value []byte, _ int64, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return l.cache.Set(key, append([]byte(nil), value...)) == nil
}

func (l *bigCacheLocal) Delete(keys ...string) {
	for _, key := range keys {
		err := l.cache.Delete(key)
		if errors.Is(err, bigcache.ErrEntryNotFound) {
			continue
		}
	}
}

func (l *bigCacheLocal) Clear() { _ = l.cache.Reset() }

func (l *bigCacheLocal) Close() error { return l.cache.Close() }
