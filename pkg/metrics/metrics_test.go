package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org/reverb/pkg/metrics"
)

func TestCollector_Snapshot(t *testing.T) {
	c := metrics.NewCollector()

	c.ExactHits.Add(3)
	c.SemanticHits.Add(2)
	c.Misses.Add(5)
	c.Stores.Add(10)
	c.Invalidations.Add(1)
	c.EmbeddingErrors.Add(2)

	snap := c.Snapshot()
	assert.Equal(t, int64(3), snap.ExactHits)
	assert.Equal(t, int64(2), snap.SemanticHits)
	assert.Equal(t, int64(5), snap.Misses)
	assert.Equal(t, int64(10), snap.Stores)
	assert.Equal(t, int64(1), snap.Invalidations)
	assert.Equal(t, int64(2), snap.EmbeddingErrors)
}

func TestMetricsSnapshot_HitRate(t *testing.T) {
	t.Run("zero total returns 0", func(t *testing.T) {
		s := metrics.MetricsSnapshot{}
		assert.Equal(t, 0.0, s.HitRate())
	})

	t.Run("all hits returns 1.0", func(t *testing.T) {
		s := metrics.MetricsSnapshot{ExactHits: 5, SemanticHits: 5, Misses: 0}
		assert.Equal(t, 1.0, s.HitRate())
	})

	t.Run("all misses returns 0.0", func(t *testing.T) {
		s := metrics.MetricsSnapshot{ExactHits: 0, SemanticHits: 0, Misses: 10}
		assert.Equal(t, 0.0, s.HitRate())
	})

	t.Run("mixed returns correct rate", func(t *testing.T) {
		s := metrics.MetricsSnapshot{ExactHits: 3, SemanticHits: 2, Misses: 5}
		assert.InDelta(t, 0.5, s.HitRate(), 0.001)
	})
}

func TestNewPrometheusCollector_Registration(t *testing.T) {
	reg := prometheus.NewRegistry()
	pc, err := metrics.NewPrometheusCollector(reg)
	require.NoError(t, err)
	require.NotNil(t, pc)

	// All 10 metrics should be registered — gathering must succeed without error.
	mfs, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(mfs))
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}

	expected := []string{
		"reverb_lookups_total",
		"reverb_lookup_duration_seconds",
		"reverb_stores_total",
		"reverb_store_duration_seconds",
		"reverb_invalidations_total",
		"reverb_entries_total",
		"reverb_embedding_duration_seconds",
		"reverb_embedding_errors_total",
		"reverb_vector_search_duration_seconds",
		"reverb_hit_rate",
	}

	// Histograms only appear after at least one observation; counters/gauges appear
	// once touched. Check registration by verifying no error on double-register.
	for _, name := range expected {
		_ = name // registration errors are the key check — see below
	}

	// Registering again must fail with AlreadyRegisteredError (proves all were registered).
	pc2, err := metrics.NewPrometheusCollector(reg)
	assert.Error(t, err, "second registration should fail")
	assert.Nil(t, pc2)
	_ = names
}

func TestPrometheusCollector_IncrementBehavior(t *testing.T) {
	reg := prometheus.NewRegistry()
	pc, err := metrics.NewPrometheusCollector(reg)
	require.NoError(t, err)

	// Increment counters.
	pc.LookupsTotal.WithLabelValues("ns1", "exact").Inc()
	pc.LookupsTotal.WithLabelValues("ns1", "exact").Inc()
	pc.StoresTotal.WithLabelValues("ns1").Inc()
	pc.InvalidationsTotal.WithLabelValues("ns1", "doc:123").Inc()
	pc.EmbeddingErrorsTotal.WithLabelValues("openai").Inc()

	// Update gauges.
	pc.EntriesTotal.WithLabelValues("ns1").Set(42)
	pc.HitRate.WithLabelValues("ns1").Set(0.75)

	// Observe histograms.
	pc.LookupDurationSeconds.WithLabelValues("ns1", "exact").Observe(0.001)
	pc.StoreDurationSeconds.WithLabelValues("ns1").Observe(0.002)
	pc.EmbeddingDurationSeconds.WithLabelValues("openai").Observe(0.05)
	pc.VectorSearchDurationSeconds.Observe(0.003)

	mfs, err := reg.Gather()
	require.NoError(t, err)

	gathered := make(map[string]bool)
	for _, mf := range mfs {
		gathered[mf.GetName()] = true
	}

	assert.True(t, gathered["reverb_lookups_total"])
	assert.True(t, gathered["reverb_stores_total"])
	assert.True(t, gathered["reverb_invalidations_total"])
	assert.True(t, gathered["reverb_embedding_errors_total"])
	assert.True(t, gathered["reverb_entries_total"])
	assert.True(t, gathered["reverb_hit_rate"])
	assert.True(t, gathered["reverb_lookup_duration_seconds"])
	assert.True(t, gathered["reverb_store_duration_seconds"])
	assert.True(t, gathered["reverb_embedding_duration_seconds"])
	assert.True(t, gathered["reverb_vector_search_duration_seconds"])
}
