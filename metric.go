package cache

type MetricType int

const (
	MetricTypeUnknown MetricType = iota
	MetricTypeMemHit
	MetricTypeMemHitExpired
	MetricTypeRedisHit
	MetricTypeRedisHitExpired
	MetricTypeMiss
)
