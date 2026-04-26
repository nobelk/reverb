package exact

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/nobelk/reverb/internal/clock"
	"github.com/nobelk/reverb/pkg/metrics"
	"github.com/nobelk/reverb/pkg/store"
)

const tracerName = "github.com/nobelk/reverb/pkg/cache/exact"

// Cache implements the exact-match (Tier 1) cache.
// It looks up entries by SHA-256 hash of (namespace + normalized_prompt + model_id).
type Cache struct {
	store store.Store
	clock clock.Clock
}

// New creates a new exact-match cache.
func New(s store.Store, clk clock.Clock) *Cache {
	if clk == nil {
		clk = clock.Real()
	}
	return &Cache{store: s, clock: clk}
}

// LookupResult holds the result of an exact-match cache lookup.
type LookupResult struct {
	Hit   bool
	Entry *store.CacheEntry
}

// Lookup checks for an exact hash match in the store.
func (c *Cache) Lookup(ctx context.Context, namespace string, hash [32]byte) (*LookupResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.exact.lookup")
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.system", "reverb"),
		attribute.String("gen_ai.cache.namespace", namespace),
	)

	entry, err := c.store.GetByHash(ctx, namespace, hash)
	if err != nil {
		metrics.RecordError(span, err)
		return nil, err
	}
	if entry == nil {
		span.SetAttributes(attribute.Bool("gen_ai.cache.hit", false))
		return &LookupResult{Hit: false}, nil
	}
	// Check expiry
	if !entry.ExpiresAt.IsZero() && c.clock.Now().After(entry.ExpiresAt) {
		span.SetAttributes(attribute.Bool("gen_ai.cache.hit", false), attribute.Bool("gen_ai.cache.expired", true))
		return &LookupResult{Hit: false}, nil
	}
	span.SetAttributes(attribute.Bool("gen_ai.cache.hit", true), attribute.String("gen_ai.cache.entry_id", entry.ID))
	return &LookupResult{Hit: true, Entry: entry}, nil
}
