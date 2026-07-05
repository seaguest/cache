package tiercache

import (
	"context"
	"time"
)

// Invalidate invalidates exact cached results identified by keys.
func (c *Client) Invalidate(ctx context.Context, keys ...Key) (err error) {
	start := time.Now()
	defer func() {
		observe(cObserver(c), "invalidate", "", start, "key", err)
	}()
	if c == nil {
		return ErrNoClient
	}
	var span spanHandle
	ctx, span = startInvalidateSpan(ctx, c, "key", len(keys))
	defer func() {
		endGetSpan(span, "key", err)
	}()
	if c.disabled {
		return nil
	}
	if c.isClosed() {
		return ErrClosed
	}
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
		names = append(names, c.keyVersionName(key))
	}
	versions, err := c.store.BumpVersions(ctx, names...)
	if err != nil {
		return err
	}
	_ = c.store.ExpireVersions(ctx, c.versionTTL(), names...)
	now := time.Now()
	for i, key := range keys {
		name := names[i]
		v := versions[name]
		c.versions.set(name, v)
		c.local.Delete(c.index.keysForKey(key, now)...)
		if err := c.store.Publish(ctx, Invalidation{
			Type:      invalidationTypeKey,
			Name:      name,
			Version:   v,
			Timestamp: now.UnixMilli(),
		}); err != nil {
			observe(c.observer, "publish", keyName(key), time.Now(), "error", err)
		}
	}
	return nil
}

// InvalidateAll invalidates the entire namespace.
func (c *Client) InvalidateAll(ctx context.Context) (err error) {
	start := time.Now()
	defer func() {
		observe(cObserver(c), "invalidate", "", start, "all", err)
	}()
	if c == nil {
		return ErrNoClient
	}
	var span spanHandle
	ctx, span = startInvalidateSpan(ctx, c, "all", 1)
	defer func() {
		endGetSpan(span, "all", err)
	}()
	if c.disabled {
		return nil
	}
	if c.isClosed() {
		return ErrClosed
	}
	name := c.namespaceVersionName()
	versions, err := c.store.BumpVersions(ctx, name)
	if err != nil {
		return err
	}
	_ = c.store.ExpireVersions(ctx, c.versionTTL(), name)
	now := time.Now()
	v := versions[name]
	c.versions.set(name, v)
	c.local.Clear()
	c.index.clear()
	if err := c.store.Publish(ctx, Invalidation{
		Type:      invalidationTypeClear,
		Name:      name,
		Version:   v,
		Timestamp: now.UnixMilli(),
	}); err != nil {
		observe(c.observer, "publish", c.namespace, time.Now(), "error", err)
	}
	return nil
}

// InvalidateTags invalidates every cached result that was read with any tag.
func (c *Client) InvalidateTags(ctx context.Context, tags ...Tag) (err error) {
	start := time.Now()
	defer func() {
		observe(cObserver(c), "invalidate", "", start, "tag", err)
	}()
	if c == nil {
		return ErrNoClient
	}
	var span spanHandle
	ctx, span = startInvalidateSpan(ctx, c, "tag", len(tags))
	defer func() {
		endGetSpan(span, "tag", err)
	}()
	if c.disabled {
		return nil
	}
	if c.isClosed() {
		return ErrClosed
	}
	tags = canonicalTags(tags)
	if err := validateTags(tags); err != nil {
		return err
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, c.tagVersionName(tag))
	}
	versions, err := c.store.BumpVersions(ctx, names...)
	if err != nil {
		return err
	}
	_ = c.store.ExpireVersions(ctx, c.versionTTL(), names...)
	now := time.Now()
	for i, tag := range tags {
		name := names[i]
		v := versions[name]
		c.versions.set(name, v)
		c.local.Delete(c.index.keysForTag(tag, now)...)
		if err := c.store.Publish(ctx, Invalidation{
			Type:      invalidationTypeTag,
			Name:      name,
			Version:   v,
			Timestamp: now.UnixMilli(),
		}); err != nil {
			observe(c.observer, "publish", string(tag), time.Now(), "error", err)
		}
	}
	return nil
}

// InvalidateScope invalidates every cached result that was read with any scope.
func (c *Client) InvalidateScope(ctx context.Context, scopes ...Scope) error {
	return c.InvalidateTags(ctx, scopes...)
}
