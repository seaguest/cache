package cache

import "github.com/gomodule/redigo/redis"

type Config struct {
	Namespace   string
	Disabled    bool
	RedisFactor int
	// parse object type from key, the key should be object_type:id by default
	GetObjectType func(key string) string
	GetConn       func() redis.Conn
	// elapsedTime in milliseconds
	OnMetric func(metric MetricType, objectType string, elapsedTime int)
	OnError  func(err error)
}
