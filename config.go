package cache

import (
	"time"

	"github.com/gomodule/redigo/redis"
)

type Config struct {
	Namespace string

	// clean interval for in-memory cache
	CleanInterval time.Duration

	// will call loader function when disabled id true
	Disabled bool

	// redis ttl = ttl*RedisFactor, data in redis lives longer than memory cache.
	RedisFactor int

	// retrieve redis connection
	GetConn func() redis.Conn

	// exposed for metrics purpose,  elapsedTime in milliseconds
	OnMetric func(metric MetricType, objectType string, elapsedTime int)

	// must be provided for cache initialization, handle internal error
	OnError func(err error)
}
