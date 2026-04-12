package exact

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/store"
)

const tracerName = "github.com/nobelk/reverb/pkg/cache/exact"

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Cache implements the exact-match (Tier 1) cache.
// It looks up entries by SHA-256 hash of (namespace + normalized_prompt + model_id).
type Cache struct {
	store store.Store
	clock Clock
}

// New creates a new exact-match cache.
func New(s store.Store, clock Clock) *Cache {
	if clock == nil {
		clock = realClock{}
	}
	return &Cache{store: s, clock: clock}
}

// LookupResult holds the result of an exact-match cache lookup.
type LookupResult struct {
	Hit   bool
	Entry *store.CacheEntry
}

// Lookup checks for an exact hash match in the store.
func (c *Cache) Lookup(ctx context.Context, namespace string, hash [32]byte) (*LookupResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "reverb.exact.lookup")
	defer span.End()
	span.SetAttributes(attribute.String("reverb.namespace", namespace))

	entry, err := c.store.GetByHash(ctx, namespace, hash)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if entry == nil {
		span.SetAttributes(attribute.Bool("reverb.hit", false))
		return &LookupResult{Hit: false}, nil
	}
	// Check expiry
	if !entry.ExpiresAt.IsZero() && c.clock.Now().After(entry.ExpiresAt) {
		span.SetAttributes(attribute.Bool("reverb.hit", false), attribute.Bool("reverb.expired", true))
		return &LookupResult{Hit: false}, nil
	}
	span.SetAttributes(attribute.Bool("reverb.hit", true), attribute.String("reverb.entry_id", entry.ID))
	return &LookupResult{Hit: true, Entry: entry}, nil
}
