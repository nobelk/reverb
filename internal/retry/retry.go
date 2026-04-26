package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Config holds retry configuration.
type Config struct {
	MaxRetries   int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	JitterFrac   float64 // e.g., 0.25 for ±25%
}

// DefaultConfig returns the default retry configuration.
func DefaultConfig() Config {
	return Config{
		MaxRetries:   3,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     5 * time.Second,
		JitterFrac:   0.25,
	}
}

// Do executes fn with exponential backoff. Returns the first successful result
// or the last error after exhausting retries.
func Do(ctx context.Context, cfg Config, fn func(ctx context.Context) error) error {
	var err error
	delay := cfg.InitialDelay
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt == cfg.MaxRetries {
			break
		}
		// Apply jitter
		jitter := 1.0 + (rand.Float64()*2-1)*cfg.JitterFrac
		actualDelay := time.Duration(float64(delay) * jitter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(actualDelay):
		}
		delay = min(delay*2, cfg.MaxDelay)
	}
	return err
}
