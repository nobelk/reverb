package metrics

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector tracks cache metrics using atomic counters.
type Collector struct {
	ExactHits       atomic.Int64
	SemanticHits    atomic.Int64
	Misses          atomic.Int64
	Stores          atomic.Int64
	Invalidations   atomic.Int64
	EmbeddingErrors atomic.Int64
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (c *Collector) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ExactHits:       c.ExactHits.Load(),
		SemanticHits:    c.SemanticHits.Load(),
		Misses:          c.Misses.Load(),
		Stores:          c.Stores.Load(),
		Invalidations:   c.Invalidations.Load(),
		EmbeddingErrors: c.EmbeddingErrors.Load(),
	}
}

// MetricsSnapshot is a point-in-time snapshot of metrics.
type MetricsSnapshot struct {
	ExactHits       int64
	SemanticHits    int64
	Misses          int64
	Stores          int64
	Invalidations   int64
	EmbeddingErrors int64
}

// HitRate returns the overall cache hit rate.
func (s MetricsSnapshot) HitRate() float64 {
	total := s.ExactHits + s.SemanticHits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.ExactHits+s.SemanticHits) / float64(total)
}

// PrometheusCollector wraps Prometheus counters, histograms, and gauges for Reverb.
type PrometheusCollector struct {
	LookupsTotal              *prometheus.CounterVec
	LookupDurationSeconds     *prometheus.HistogramVec
	StoresTotal               *prometheus.CounterVec
	StoreDurationSeconds      *prometheus.HistogramVec
	InvalidationsTotal        *prometheus.CounterVec
	EntriesTotal              *prometheus.GaugeVec
	EmbeddingDurationSeconds  *prometheus.HistogramVec
	EmbeddingErrorsTotal      *prometheus.CounterVec
	VectorSearchDurationSeconds prometheus.Histogram
	HitRate                   *prometheus.GaugeVec
}

// NewPrometheusCollector creates and registers all Reverb Prometheus metrics.
// Returns an error if any metric fails to register.
func NewPrometheusCollector(reg prometheus.Registerer) (*PrometheusCollector, error) {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	pc := &PrometheusCollector{
		LookupsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reverb_lookups_total",
			Help: "Total number of cache lookups.",
		}, []string{"namespace", "tier"}),

		LookupDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "reverb_lookup_duration_seconds",
			Help:    "Duration of cache lookups in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"namespace", "tier"}),

		StoresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reverb_stores_total",
			Help: "Total number of cache store operations.",
		}, []string{"namespace"}),

		StoreDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "reverb_store_duration_seconds",
			Help:    "Duration of cache store operations in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"namespace"}),

		InvalidationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reverb_invalidations_total",
			Help: "Total number of cache invalidations.",
		}, []string{"namespace", "source_id"}),

		EntriesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "reverb_entries_total",
			Help: "Current number of entries in the cache.",
		}, []string{"namespace"}),

		EmbeddingDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "reverb_embedding_duration_seconds",
			Help:    "Duration of embedding generation in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"provider"}),

		EmbeddingErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reverb_embedding_errors_total",
			Help: "Total number of embedding generation errors.",
		}, []string{"provider"}),

		VectorSearchDurationSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "reverb_vector_search_duration_seconds",
			Help:    "Duration of vector similarity search in seconds.",
			Buckets: prometheus.DefBuckets,
		}),

		HitRate: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "reverb_hit_rate",
			Help: "Rolling cache hit rate per namespace.",
		}, []string{"namespace"}),
	}

	collectors := []prometheus.Collector{
		pc.LookupsTotal,
		pc.LookupDurationSeconds,
		pc.StoresTotal,
		pc.StoreDurationSeconds,
		pc.InvalidationsTotal,
		pc.EntriesTotal,
		pc.EmbeddingDurationSeconds,
		pc.EmbeddingErrorsTotal,
		pc.VectorSearchDurationSeconds,
		pc.HitRate,
	}

	for _, col := range collectors {
		if err := reg.Register(col); err != nil {
			return nil, err
		}
	}

	return pc, nil
}
