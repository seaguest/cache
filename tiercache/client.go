package tiercache

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"
)

const defaultNamespace = "tiercache"

// New constructs a Client from cfg.
func New(cfg Config) (*Client, error) {
	ns := strings.TrimSpace(cfg.Namespace)
	if ns == "" {
		ns = defaultNamespace
	}
	if cfg.Store == nil {
		return nil, ErrNoStore
	}
	if cfg.Local == nil {
		local, err := NewRistrettoLocal(RistrettoConfig{MaxCost: 64 << 20})
		if err != nil {
			return nil, err
		}
		cfg.Local = local
	}
	if cfg.Codec == nil {
		cfg.Codec = JSONCodec{}
	}
	if cfg.Observer == nil {
		cfg.Observer = noopObserver{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	defaults := cfg.Defaults
	if defaults.TTL == 0 {
		defaults.TTL = time.Minute
	}
	if defaults.Stale == 0 {
		defaults.Stale = time.Minute
	}
	if defaults.NegativeTTL == 0 {
		defaults.NegativeTTL = 30 * time.Second
	}
	if defaults.Consistency == ConsistencyDefault {
		defaults.Consistency = ConsistencyFast
	}
	if !defaults.Policy.StaleWhileRevalidate && !defaults.Policy.StaleIfError {
		defaults.Policy.StaleWhileRevalidate = true
		defaults.Policy.StaleIfError = true
	}

	limits := cfg.Limits
	if limits.MaxTTL == 0 {
		limits.MaxTTL = 24 * time.Hour
	}
	if limits.MaxStale == 0 {
		limits.MaxStale = time.Hour
	}
	if limits.RefreshTimeout == 0 {
		limits.RefreshTimeout = 10 * time.Second
	}
	if limits.RefreshConcurrency <= 0 {
		limits.RefreshConcurrency = 32
	}
	if limits.VersionCacheTTL == 0 {
		limits.VersionCacheTTL = time.Second
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
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		namespace:  ns,
		store:      cfg.Store,
		local:      cfg.Local,
		codec:      cfg.Codec,
		defaults:   defaults,
		limits:     limits,
		failOpen:   cfg.FailOpen,
		disabled:   cfg.Disabled,
		stampede:   stampede,
		observer:   cfg.Observer,
		logger:     cfg.Logger,
		tracer:     cfg.Tracer,
		versions:   newVersionCache(limits.VersionCacheTTL),
		index:      newLocalIndex(),
		refreshSem: make(chan struct{}, limits.RefreshConcurrency),
		ctx:        ctx,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	go c.watchInvalidations()
	return c, nil
}

// Close stops background invalidation handling and closes the local and remote
// adapters.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.cancel()
	<-c.done
	errLocal := c.local.Close()
	errStore := c.store.Close()
	if errLocal != nil {
		return errLocal
	}
	return errStore
}

func (c *Client) isClosed() bool {
	select {
	case <-c.ctx.Done():
		return true
	default:
		return false
	}
}

func (c *Client) watchInvalidations() {
	defer close(c.done)
	backoff := 100 * time.Millisecond
	for {
		if c.ctx.Err() != nil {
			return
		}
		err := c.store.Subscribe(c.ctx, c.handleInvalidation)
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.logger.Warn("tiercache invalidation subscription failed", "error", err)
			observe(c.observer, "subscribe", "", time.Now(), "error", err)
			c.local.Clear()
			c.index.clear()
			c.versions.clear()
			time.Sleep(backoff)
			if backoff < 5*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 100 * time.Millisecond
	}
}

func (c *Client) handleInvalidation(event Invalidation) {
	if event.Type == invalidationTypeClear {
		if event.Name == c.namespaceVersionName() {
			c.setLocalVersion(event.Name, event.Version)
		}
		c.local.Clear()
		c.index.clear()
		return
	}
	if event.Name == "" {
		return
	}
	switch event.Type {
	case invalidationTypeKey:
		c.setLocalVersion(event.Name, event.Version)
	case invalidationTypeTag:
		c.setLocalVersion(event.Name, event.Version)
	}
}
