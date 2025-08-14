package cache

import (
	"context"
	"time"

	"github.com/gomodule/redigo/redis"
)

type GetCachePolicy int

const (
	GetPolicyReturnExpired GetCachePolicy = iota + 1
	GetPolicyReloadOnExpiry
)

type Options struct {
	Namespace string

	// key should be in format object_type{Separator}id
	// can be : or ; or #
	Separator string

	// clean interval for in-memory cache
	CleanInterval time.Duration

	// get policy when data is expired, ReturnExpired or ReloadOnExpiry
	GetPolicy GetCachePolicy

	// will call loader function when disabled id true
	Disabled bool

	// redis ttl = ttl*RedisTTLFactor, data in redis lives longer than memory cache.
	RedisTTLFactor int

	// retrieve redis connection
	GetConn func() redis.Conn

	// metrics
	Metric Metrics

	// must be provided for cache initialization, handle internal error
	OnError func(ctx context.Context, err error)
}

type Option func(*Options)

func Namespace(namespace string) Option {
	return func(o *Options) {
		o.Namespace = namespace
	}
}

func Separator(separator string) Option {
	return func(o *Options) {
		o.Separator = separator
	}
}

func CleanInterval(cleanInterval time.Duration) Option {
	return func(o *Options) {
		o.CleanInterval = cleanInterval
	}
}

func Disabled(disabled bool) Option {
	return func(o *Options) {
		o.Disabled = disabled
	}
}

func RedisTTLFactor(redisTTLFactor int) Option {
	return func(o *Options) {
		o.RedisTTLFactor = redisTTLFactor
	}
}

func GetConn(getConn func() redis.Conn) Option {
	return func(o *Options) {
		o.GetConn = getConn
	}
}

func OnMetric(onMetric func(key, objectType string, metricType string, count int, elapsedTime time.Duration)) Option {
	return func(o *Options) {
		o.Metric = Metrics{
			onMetric: onMetric,
		}
	}
}

func OnError(onError func(ctx context.Context, err error)) Option {
	return func(o *Options) {
		o.OnError = onError
	}
}

func GetPolicy(getPolicy GetCachePolicy) Option {
	return func(o *Options) {
		o.GetPolicy = getPolicy
	}
}

func newOptions(opts ...Option) Options {
	opt := Options{}
	for _, o := range opts {
		o(&opt)
	}
	return opt
}
