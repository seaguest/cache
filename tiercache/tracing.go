package tiercache

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type spanHandle struct {
	span trace.Span
}

func startGetSpan(ctx context.Context, c *Client, name string, key Key, tags []Tag) (context.Context, spanHandle) {
	if c == nil || c.tracer == nil {
		return ctx, spanHandle{}
	}
	return startCacheSpan(ctx, c, "tiercache.get",
		attribute.String("cache.name", name),
		attribute.String("cache.key_hash", hashHex([]byte(key))),
		attribute.Int("cache.tag_count", len(tags)),
	)
}

func startGetManySpan(ctx context.Context, c *Client, name string, itemCount int) (context.Context, spanHandle) {
	return startCacheSpan(ctx, c, "tiercache.get_many",
		attribute.String("cache.name", name),
		attribute.Int("cache.item_count", itemCount),
	)
}

func startInvalidateSpan(ctx context.Context, c *Client, typ string, count int) (context.Context, spanHandle) {
	return startCacheSpan(ctx, c, "tiercache.invalidate",
		attribute.String("cache.invalidate_type", typ),
		attribute.Int("cache.item_count", count),
	)
}

func startCacheSpan(ctx context.Context, c *Client, spanName string, attrs ...attribute.KeyValue) (context.Context, spanHandle) {
	if c == nil || c.tracer == nil {
		return ctx, spanHandle{}
	}
	base := []attribute.KeyValue{attribute.String("cache.namespace", c.namespace)}
	base = append(base, attrs...)
	ctx, span := c.tracer.Start(ctx, spanName, trace.WithAttributes(base...))
	return ctx, spanHandle{span: span}
}

func endGetSpan(h spanHandle, result string, err error) {
	if h.span == nil {
		return
	}
	if result != "" {
		h.span.SetAttributes(attribute.String("cache.result", result))
	}
	if err != nil {
		h.span.RecordError(err)
		h.span.SetStatus(codes.Error, err.Error())
	}
	h.span.End()
}
