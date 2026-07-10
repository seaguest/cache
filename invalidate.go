package cache

import (
	"context"
	"fmt"
	"time"
)

// Invalidate advances the generation of exact logical keys.
func (c *Client) Invalidate(ctx context.Context, keys ...Key) (err error) {
	if err = c.begin(); err != nil {
		return err
	}
	defer c.end()
	start := time.Now()
	defer func() { observe(c.observer, "invalidate", "", start, "key", err) }()
	if c.disabled || len(keys) == 0 {
		return nil
	}
	if len(keys) > c.limits.MaxBatchSize {
		return ErrInvalidConfig
	}
	names := make([]string, 0, len(keys))
	seen := make(map[Key]struct{}, len(keys))
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, c.keyVersionName(key))
	}
	return c.bumpAndPublish(ctx, InvalidationKey, names)
}

// InvalidateTags advances every supplied dependency generation. Any cached
// result associated with at least one tag becomes unreachable.
func (c *Client) InvalidateTags(ctx context.Context, tags ...Tag) (err error) {
	if err = c.begin(); err != nil {
		return err
	}
	defer c.end()
	start := time.Now()
	defer func() { observe(c.observer, "invalidate", "", start, "tag", err) }()
	if c.disabled || len(tags) == 0 {
		return nil
	}
	if len(tags) > c.limits.MaxBatchSize {
		return ErrInvalidConfig
	}
	tags = canonicalTags(tags)
	if err := validateTags(tags); err != nil {
		return err
	}
	names := make([]string, len(tags))
	for i, tag := range tags {
		names[i] = c.tagVersionName(tag)
	}
	return c.bumpAndPublish(ctx, InvalidationTag, names)
}

// InvalidateAll advances the namespace generation and clears this process's
// local cache.
func (c *Client) InvalidateAll(ctx context.Context) (err error) {
	if err = c.begin(); err != nil {
		return err
	}
	defer c.end()
	start := time.Now()
	defer func() { observe(c.observer, "invalidate", "", start, "namespace", err) }()
	if c.disabled {
		return nil
	}
	err = c.bumpAndPublish(ctx, InvalidationNamespace, []string{c.namespaceVersionName()})
	if err == nil {
		c.local.Clear()
	}
	return err
}

func (c *Client) bumpAndPublish(ctx context.Context, kind InvalidationKind, names []string) error {
	versions, err := c.store.BumpVersions(ctx, names...)
	if err != nil {
		return err
	}
	now := time.Now()
	bus, hasBus := c.store.(InvalidationBus)
	for _, name := range names {
		version := versions[name]
		if version <= 0 {
			return fmt.Errorf("%w: store returned invalid generation for %s", ErrInvalidConfig, name)
		}
		c.versions.setMax(name, version, now)
		if hasBus {
			if publishErr := bus.Publish(ctx, c.channel, Invalidation{Kind: kind, Name: name, Version: version}); publishErr != nil {
				observe(c.observer, "publish", "", time.Now(), "error", publishErr)
			}
		}
	}
	return nil
}
