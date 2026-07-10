package cache

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done   chan struct{}
	value  any
	err    error
	shared bool
}

func (g *flightGroup) do(ctx context.Context, key string, start func(), finish func(), fn func() (any, error)) (any, error, bool) {
	g.mu.Lock()
	if call := g.calls[key]; call != nil {
		call.shared = true
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err(), true
		case <-call.done:
			return call.value, call.err, true
		}
	}
	if g.calls == nil {
		g.calls = make(map[string]*flightCall)
	}
	call := &flightCall{done: make(chan struct{})}
	g.calls[key] = call
	start()
	g.mu.Unlock()

	go func() {
		defer func() {
			if value := recover(); value != nil {
				call.err = LoaderPanicError{Value: value, Stack: debug.Stack()}
			}
			g.mu.Lock()
			delete(g.calls, key)
			close(call.done)
			g.mu.Unlock()
			finish()
		}()
		call.value, call.err = fn()
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err(), false
	case <-call.done:
		return call.value, call.err, call.shared
	}
}

// LoaderPanicError converts a detached loader panic into a normal error so it
// cannot crash the process or strand flight waiters.
type LoaderPanicError struct {
	Value any
	Stack []byte
}

func (e LoaderPanicError) Error() string { return fmt.Sprintf("cache: loader panic: %v", e.Value) }
