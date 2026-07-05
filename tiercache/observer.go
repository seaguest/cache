package tiercache

import (
	"sync"
	"time"
)

type ObserverFunc func(Observation)

func (f ObserverFunc) Observe(o Observation) {
	if f != nil {
		f(o)
	}
}

type noopObserver struct{}

func (noopObserver) Observe(Observation) {}

func observe(o Observer, op, name string, start time.Time, result string, err error) {
	if o == nil {
		return
	}
	o.Observe(Observation{
		Operation: op,
		Name:      name,
		Result:    result,
		Duration:  time.Since(start),
		Err:       err,
	})
}

type StatsObserver struct {
	mu     sync.Mutex
	counts map[StatsKey]uint64
	total  map[StatsKey]time.Duration
}

// StatsKey groups observations by operation and result.
type StatsKey struct {
	Operation string
	Result    string
}

// StatsSnapshot is a point-in-time copy of StatsObserver counters.
type StatsSnapshot struct {
	Counts        map[StatsKey]uint64
	TotalDuration map[StatsKey]time.Duration
}

// NewStatsObserver returns an in-memory observer useful for tests and simple
// rollout checks.
func NewStatsObserver() *StatsObserver {
	return &StatsObserver{
		counts: make(map[StatsKey]uint64),
		total:  make(map[StatsKey]time.Duration),
	}
}

func (s *StatsObserver) Observe(o Observation) {
	if s == nil {
		return
	}
	key := StatsKey{Operation: o.Operation, Result: o.Result}
	s.mu.Lock()
	s.counts[key]++
	s.total[key] += o.Duration
	s.mu.Unlock()
}

// Snapshot returns a copy of the accumulated observations.
func (s *StatsObserver) Snapshot() StatsSnapshot {
	out := StatsSnapshot{
		Counts:        make(map[StatsKey]uint64),
		TotalDuration: make(map[StatsKey]time.Duration),
	}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.counts {
		out.Counts[k] = v
	}
	for k, v := range s.total {
		out.TotalDuration[k] = v
	}
	return out
}
