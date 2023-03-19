package cache

type MetricType int

const (
	MetricTypeMemHit MetricType = iota
	MetricTypeMemHitExpired
	MetricTypeRedisHit
	MetricTypeRedisHitExpired
	MetricTypeMiss
)
