package reverb

import (
	"log/slog"

	"github.com/nobelk/reverb/pkg/cdc"
	"github.com/nobelk/reverb/pkg/metrics"
	"github.com/nobelk/reverb/pkg/normalize"
)

// Option is a functional option for configuring a Client.
type Option func(*Client)

// WithClock overrides the clock used by the client (useful in tests).
func WithClock(clock Clock) Option {
	return func(c *Client) {
		c.clock = clock
	}
}

// WithLogger sets the structured logger for the client.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

// WithCDCListener sets the CDC listener that feeds change events into the
// invalidation loop. The listener is started when the client is created.
func WithCDCListener(listener cdc.Listener) Option {
	return func(c *Client) {
		c.cdcListener = listener
	}
}

// WithMetricsCollector replaces the default metrics collector with the
// provided one. This allows sharing a collector across multiple clients
// or injecting a pre-populated collector in tests.
func WithMetricsCollector(collector *metrics.Collector) Option {
	return func(c *Client) {
		c.collector = collector
	}
}

// WithPrometheusCollector attaches a PrometheusCollector so that Lookup,
// Store, and Invalidate record counters and duration histograms alongside
// the internal atomic counters. When nil, no Prometheus recording happens
// and metric families remain empty on the /metrics endpoint.
func WithPrometheusCollector(pc *metrics.PrometheusCollector) Option {
	return func(c *Client) {
		c.prom = pc
	}
}

// WithTracer replaces the default OTel tracer with the provided one.
func WithTracer(tracer *metrics.Tracer) Option {
	return func(c *Client) {
		c.tracer = tracer
	}
}

// WithDefaultRedactor sets a Redactor that is applied to every namespace
// not explicitly overridden via WithNamespaceRedactor. Pass nil to disable.
//
// The redactor runs between normalize.Normalize and the SHA-256 cache key.
// Enabling redaction on an existing cache invalidates prior entries by
// construction (the hash changes). Operators should expect a one-time hit-
// rate drop and either drain the cache or accept the natural rebuild.
func WithDefaultRedactor(r normalize.Redactor) Option {
	return func(c *Client) {
		c.defaultRedactor = r
	}
}

// WithNamespaceRedactor sets a per-namespace redactor override. The
// override takes precedence over WithDefaultRedactor. Pass a nil redactor
// to explicitly disable redaction for namespace.
func WithNamespaceRedactor(namespace string, r normalize.Redactor) Option {
	return func(c *Client) {
		if c.redactors == nil {
			c.redactors = make(map[string]normalize.Redactor)
		}
		c.redactors[namespace] = r
	}
}

// WithRebuildVectorIndex, when true, causes New to scan the store and re-add
// every non-expired entry's embedding to the vector index before returning.
//
// Reverb's vector index (flat, hnsw) is in-memory only. When the store is a
// durable backend (badger, redis) that outlives the process, restarts leave
// the index empty until new Store calls re-populate it — exact-tier lookups
// still hit, but semantic lookups silently miss until warmup completes.
// Enabling this option closes that gap at the cost of an O(N) scan at
// startup. Use with memory-backed stores is harmless but pointless.
//
// If the scan fails, New returns an error and the client is not usable.
func WithRebuildVectorIndex(rebuild bool) Option {
	return func(c *Client) {
		c.rebuildOnStart = rebuild
	}
}
