package cache

import (
	"strings"
	"time"
)

const (
	MetricTypeGetMemHit       = "get_mem_hit"
	MetricTypeGetMemMiss      = "get_mem_miss"
	MetricTypeGetMemExpired   = "get_mem_expired"
	MetricTypeGetRedisHit     = "get_redis_hit"
	MetricTypeGetRedisMiss    = "get_redis_miss"
	MetricTypeGetRedisExpired = "get_redis_expired"
	MetricTypeGetCache        = "get_cache"
	MetricTypeLoad            = "load"
	MetricTypeAsyncLoad       = "async_load"
	MetricTypeSetCache        = "set_cache"
	MetricTypeSetMem          = "set_mem"
	MetricTypeSetRedis        = "set_redis"
	MetricTypeDeleteCache     = "del_cache"
	MetricTypeDeleteMem       = "del_mem"
	MetricTypeDeleteRedis     = "del_redis"
	MetricTypeCount           = "count"
	MetricTypeMemUsage        = "mem_usage"
)

type Metrics struct {
	// keys are namespacedKey, need trim namespace
	namespace string

	separator string

	onMetric func(key, objectType string, metricType string, count int, elapsedTime time.Duration)
}

// Observe used for histogram metrics
func (m Metrics) Observe() func(string, interface{}, *error) {
	start := time.Now()
	return func(namespacedKey string, metricType interface{}, err *error) {
		if m.onMetric == nil {
			return
		}
		// ignore metric for error case
		if err != nil && *err != nil {
			return
		}

		var metric string
		switch v := metricType.(type) {
		case *string:
			metric = *v
		case string:
			metric = v
		default:
			return
		}
		key := strings.TrimPrefix(namespacedKey, m.namespace+":")
		objectType := strings.Split(key, m.separator)[0]
		m.onMetric(key, objectType, metric, 0, time.Since(start))
	}
}

// Set used for gauge metrics, counts and memory usage metrics
func (m Metrics) Set(objectType, metric string, count int) {
	m.onMetric("*", objectType, metric, count, 0)
}
