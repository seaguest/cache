package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"
)

type Client struct {
	namespace        string
	channel          string
	dataPrefix       string
	namespaceVersion string
	store            Store
	local            LocalCache
	codec            Codec

	defaults    Defaults
	limits      Limits
	failureMode FailureMode
	consistency Consistency
	disabled    bool
	stampede    StampedeProtection
	observer    Observer
	logger      *slog.Logger

	versions *versionCache
	routes   *routeCache
	flights  flightGroup
	refresh  chan struct{}

	ctx    context.Context
	cancel context.CancelFunc

	lifecycleMu sync.Mutex
	closed      bool
	active      sync.WaitGroup
	workers     sync.WaitGroup
	closeOnce   sync.Once
	closeDone   chan struct{}
	closeErr    error
}

// New constructs one process-wide cache client.
func New(cfg Config) (*Client, error) {
	namespace := strings.TrimSpace(cfg.Namespace)
	if namespace == "" || len(namespace) > 128 {
		return nil, ErrInvalidNamespace
	}
	if !cfg.Disabled && cfg.Store == nil {
		return nil, ErrNoStore
	}
	if cfg.Local == nil {
		cfg.Local = NewLocalCache(64 << 20)
	}
	if cfg.Codec == nil {
		cfg.Codec = JSONCodec{}
	}
	if strings.TrimSpace(cfg.Codec.Name()) == "" || len(cfg.Codec.Name()) > 128 {
		return nil, ErrInvalidCodec
	}
	if cfg.Observer == nil {
		cfg.Observer = noopObserver{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	defaults := cfg.Defaults
	if defaults.TTL == 0 {
		defaults.TTL = time.Minute
	}
	if defaults.TTL < time.Millisecond || defaults.NegativeTTL < 0 {
		return nil, fmt.Errorf("%w: invalid defaults", ErrInvalidConfig)
	}

	limits := cfg.Limits
	if limits.MaxTTL == 0 {
		limits.MaxTTL = 24 * time.Hour
	}
	if limits.MaxStale == 0 {
		limits.MaxStale = time.Hour
	}
	if limits.LoadTimeout == 0 {
		limits.LoadTimeout = 30 * time.Second
	}
	if limits.RefreshTimeout == 0 {
		limits.RefreshTimeout = 10 * time.Second
	}
	if limits.RefreshConcurrency == 0 {
		limits.RefreshConcurrency = 32
	}
	if limits.VersionCacheTTL == 0 {
		limits.VersionCacheTTL = time.Second
	}
	if limits.VersionCacheEntries == 0 {
		limits.VersionCacheEntries = 64 * 1024
	}
	if limits.RouteCacheEntries == 0 {
		limits.RouteCacheEntries = 16 * 1024
	}
	if limits.MaxTagsPerEntry == 0 {
		limits.MaxTagsPerEntry = 32
	}
	if limits.MaxBatchSize == 0 {
		limits.MaxBatchSize = 1000
	}
	if limits.MaxTTL < time.Millisecond || limits.MaxStale < 0 || limits.LoadTimeout <= 0 ||
		limits.RefreshTimeout <= 0 || limits.RefreshConcurrency < 1 || limits.VersionCacheTTL <= 0 ||
		limits.VersionCacheEntries < 64 || limits.RouteCacheEntries < 64 ||
		limits.MaxTagsPerEntry < 1 || limits.MaxTagsPerEntry > 32 || limits.MaxBatchSize < 1 {
		return nil, fmt.Errorf("%w: invalid limits", ErrInvalidConfig)
	}
	if limits.MaxTTL > time.Duration(math.MaxInt64)-limits.MaxStale {
		return nil, fmt.Errorf("%w: ttl and stale limits overflow", ErrInvalidConfig)
	}
	if defaults.TTL > limits.MaxTTL || defaults.NegativeTTL > limits.MaxTTL {
		return nil, fmt.Errorf("%w: defaults exceed limits", ErrInvalidConfig)
	}
	if cfg.FailureMode != FailOpen && cfg.FailureMode != FailClosed {
		return nil, fmt.Errorf("%w: unknown failure mode", ErrInvalidConfig)
	}
	if cfg.Consistency != Fast && cfg.Consistency != Strict {
		return nil, fmt.Errorf("%w: unknown consistency", ErrInvalidConfig)
	}

	stampede := cfg.Stampede
	if stampede.Distributed {
		if _, ok := cfg.Store.(LockStore); !ok {
			return nil, ErrNoLockStore
		}
		if stampede.LockTTL == 0 {
			stampede.LockTTL = 3 * time.Second
		}
		if stampede.WaitTimeout == 0 {
			stampede.WaitTimeout = 100 * time.Millisecond
		}
		if stampede.PollInterval == 0 {
			stampede.PollInterval = 10 * time.Millisecond
		}
		if stampede.LockTTL <= 0 || stampede.WaitTimeout <= 0 || stampede.PollInterval <= 0 {
			return nil, fmt.Errorf("%w: invalid stampede protection", ErrInvalidConfig)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		namespace:        namespace,
		channel:          namespace + ":invalidate",
		dataPrefix:       namespace + ":data:",
		namespaceVersion: namespace + ":version:namespace",
		store:            cfg.Store,
		local:            cfg.Local,
		codec:            cfg.Codec,
		defaults:         defaults,
		limits:           limits,
		failureMode:      cfg.FailureMode,
		consistency:      cfg.Consistency,
		disabled:         cfg.Disabled,
		stampede:         stampede,
		observer:         cfg.Observer,
		logger:           cfg.Logger,
		versions:         newVersionCache(limits.VersionCacheTTL, limits.VersionCacheEntries),
		routes:           newRouteCache(limits.RouteCacheEntries),
		refresh:          make(chan struct{}, limits.RefreshConcurrency),
		ctx:              ctx,
		cancel:           cancel,
		closeDone:        make(chan struct{}),
	}
	if !c.disabled {
		if _, ok := c.store.(InvalidationBus); ok {
			c.workers.Add(1)
			go c.watchInvalidations()
		}
	}
	return c, nil
}

func (c *Client) begin() error {
	if c == nil {
		return ErrNoClient
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.active.Add(1)
	return nil
}

func (c *Client) end() { c.active.Done() }

// Close stops background work, waits for active operations, then closes owned
// adapters. It is safe to call more than once.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.lifecycleMu.Lock()
		c.closed = true
		c.cancel()
		c.lifecycleMu.Unlock()
		c.active.Wait()
		c.workers.Wait()
		var errs []error
		if c.local != nil {
			errs = append(errs, c.local.Close())
		}
		if c.store != nil {
			errs = append(errs, c.store.Close())
		}
		c.closeErr = errors.Join(errs...)
		close(c.closeDone)
	})
	<-c.closeDone
	return c.closeErr
}

func (c *Client) watchInvalidations() {
	defer c.workers.Done()
	bus := c.store.(InvalidationBus)
	backoff := 100 * time.Millisecond
	for c.ctx.Err() == nil {
		err := bus.Subscribe(c.ctx, c.channel, c.handleInvalidation)
		if c.ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("subscription ended")
		}
		c.logger.Warn("cache invalidation subscription failed", "error", err)
		observe(c.observer, "subscribe", "", time.Now(), "error", err)
		c.local.Clear()
		c.versions.clear()
		timer := time.NewTimer(backoff)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) handleInvalidation(event Invalidation) {
	if event.Name == "" || event.Version < 0 {
		return
	}
	c.versions.setMax(event.Name, event.Version, time.Now())
	if event.Kind == InvalidationNamespace {
		c.local.Clear()
	}
}
