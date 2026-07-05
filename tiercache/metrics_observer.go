package tiercache

const (
	MetricGetTotal                = "tiercache_get_total"
	MetricL1HitTotal              = "tiercache_l1_hit_total"
	MetricL1MissTotal             = "tiercache_l1_miss_total"
	MetricL2HitTotal              = "tiercache_l2_hit_total"
	MetricL2MissTotal             = "tiercache_l2_miss_total"
	MetricLoadTotal               = "tiercache_load_total"
	MetricLoadDurationSeconds     = "tiercache_load_duration_seconds"
	MetricDecodeErrorTotal        = "tiercache_decode_error_total"
	MetricEncodeErrorTotal        = "tiercache_encode_error_total"
	MetricStaleReturnTotal        = "tiercache_stale_return_total"
	MetricNegativeHitTotal        = "tiercache_negative_hit_total"
	MetricInvalidateTotal         = "tiercache_invalidate_total"
	MetricPubSubReconnectTotal    = "tiercache_pubsub_reconnect_total"
	MetricLocalEvictionTotal      = "tiercache_local_eviction_total"
	MetricSingleflightSharedTotal = "tiercache_singleflight_shared_total"
)

type MetricLabels map[string]string

// MetricRecorder is the small metrics adapter seam used by MetricsObserver.
type MetricRecorder interface {
	IncCounter(name string, labels MetricLabels)
	ObserveHistogram(name string, value float64, labels MetricLabels)
}

// MetricsObserver maps tiercache observations to stable metric names.
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
	case "local":
		if o.Result == "eviction" {
			m.Recorder.IncCounter(MetricLocalEvictionTotal, MetricLabels{"cache": cache})
		}
	case "singleflight":
		if o.Result == "shared" {
			m.Recorder.IncCounter(MetricSingleflightSharedTotal, MetricLabels{"cache": cache})
		}
	}
}
