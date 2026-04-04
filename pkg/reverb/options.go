package reverb

import (
	"log/slog"

	"github.com/org/reverb/pkg/cdc"
	"github.com/org/reverb/pkg/metrics"
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
