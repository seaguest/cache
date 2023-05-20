package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

type Options struct {
	Namespace string

	// clean interval for in-memory cache
	CleanInterval time.Duration

	// will call loader function when disabled id true
	Disabled bool

	// redis ttl = ttl*RedisTTLFactor, data in redis lives longer than memory cache.
	RedisTTLFactor int

	// retrieve redis connection
	GetConn func() redis.Conn

	// metrics
	Metric Metrics

	// must be provided for cache initialization, handle internal error
	OnError func(err error)
}

type Option func(*Options)

func Namespace(namespace string) Option {
	return func(o *Options) {
		o.Namespace = namespace
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

func OnMetric(onMetric func(key string, metricType string, elapsedTime time.Duration)) Option {
	return func(o *Options) {
		o.Metric = Metrics{
			onMetric: onMetric,
		}
	}
}

func OnError(onError func(err error)) Option {
	return func(o *Options) {
		o.OnError = onError
	}
}

func newOptions(opts ...Option) Options {
	opt := Options{}
	for _, o := range opts {
		o(&opt)
	}
	return opt
}
