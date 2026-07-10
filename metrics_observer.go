package cache

const (
	MetricGetTotal                = "cache_get_total"
	MetricL1HitTotal              = "cache_l1_hit_total"
	MetricL1MissTotal             = "cache_l1_miss_total"
	MetricL2HitTotal              = "cache_l2_hit_total"
	MetricL2MissTotal             = "cache_l2_miss_total"
	MetricLoadTotal               = "cache_load_total"
	MetricLoadDurationSeconds     = "cache_load_duration_seconds"
	MetricDecodeErrorTotal        = "cache_decode_error_total"
	MetricEncodeErrorTotal        = "cache_encode_error_total"
	MetricStaleReturnTotal        = "cache_stale_return_total"
	MetricNegativeHitTotal        = "cache_negative_hit_total"
	MetricInvalidateTotal         = "cache_invalidate_total"
	MetricPubSubReconnectTotal    = "cache_pubsub_reconnect_total"
	MetricSingleflightSharedTotal = "cache_singleflight_shared_total"
)

type MetricLabels map[string]string

// MetricRecorder is the small metrics adapter seam used by MetricsObserver.
type MetricRecorder interface {
	IncCounter(name string, labels MetricLabels)
	ObserveHistogram(name string, value float64, labels MetricLabels)
}

// MetricsObserver maps cache observations to stable metric names.
type MetricsObserver struct {
	Recorder MetricRecorder
}

func (m MetricsObserver) Observe(o Observation) {
	if m.Recorder == nil {
		return
	}
	cache := o.Name
	if cache == "" {
		cache = "unknown"
	}
	switch o.Operation {
	case "get", "get_many":
		m.Recorder.IncCounter(MetricGetTotal, MetricLabels{"cache": cache, "result": o.Result})
		if o.Result == "l1_negative" || o.Result == "l2_negative" {
			m.Recorder.IncCounter(MetricNegativeHitTotal, MetricLabels{"cache": cache})
		}
	case "l1":
		if o.Result == "hit" {
			m.Recorder.IncCounter(MetricL1HitTotal, MetricLabels{"cache": cache})
		}
		if o.Result == "miss" {
			m.Recorder.IncCounter(MetricL1MissTotal, MetricLabels{"cache": cache})
		}
	case "l2":
		if o.Result == "hit" {
			m.Recorder.IncCounter(MetricL2HitTotal, MetricLabels{"cache": cache})
		}
		if o.Result == "miss" {
			m.Recorder.IncCounter(MetricL2MissTotal, MetricLabels{"cache": cache})
		}
	case "load", "load_many":
		m.Recorder.IncCounter(MetricLoadTotal, MetricLabels{"cache": cache, "result": o.Result})
		m.Recorder.ObserveHistogram(MetricLoadDurationSeconds, o.Duration.Seconds(), MetricLabels{"cache": cache})
	case "decode":
		if o.Err != nil || o.Result == "error" {
			m.Recorder.IncCounter(MetricDecodeErrorTotal, MetricLabels{"cache": cache})
		}
	case "encode":
		if o.Err != nil || o.Result == "error" {
			m.Recorder.IncCounter(MetricEncodeErrorTotal, MetricLabels{"cache": cache})
		}
	case "stale":
		m.Recorder.IncCounter(MetricStaleReturnTotal, MetricLabels{"cache": cache, "result": o.Result})
	case "invalidate":
		m.Recorder.IncCounter(MetricInvalidateTotal, MetricLabels{"type": o.Result})
	case "subscribe":
		if o.Err != nil || o.Result == "error" {
			m.Recorder.IncCounter(MetricPubSubReconnectTotal, nil)
		}
	case "singleflight":
		if o.Result == "shared" {
			m.Recorder.IncCounter(MetricSingleflightSharedTotal, MetricLabels{"cache": cache})
		}
	}
}
