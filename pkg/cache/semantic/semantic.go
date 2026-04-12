package semantic

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/vector"
)

const tracerName = "github.com/nobelk/reverb/pkg/cache/semantic"

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Config holds semantic cache configuration.
type Config struct {
	Threshold    float32 // minimum cosine similarity for a hit
	TopK         int     // max candidates to retrieve
	ScopeByModel bool    // whether to filter results by model ID
}

// Cache implements the semantic (Tier 2) cache.
type Cache struct {
	embedder embedding.Provider
	index    vector.Index
	store    store.Store
	cfg      Config
	clock    Clock
}

// New creates a new semantic cache.
func New(embedder embedding.Provider, idx vector.Index, s store.Store, cfg Config, clock Clock) *Cache {
	if clock == nil {
		clock = realClock{}
	}
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.95
	}
	if cfg.TopK == 0 {
		cfg.TopK = 5
	}
	return &Cache{
		embedder: embedder,
		index:    idx,
		store:    s,
		cfg:      cfg,
		clock:    clock,
	}
}

// LookupResult holds the result of a semantic cache lookup.
type LookupResult struct {
	Hit        bool
	Entry      *store.CacheEntry
	Similarity float32
}

// Lookup searches the vector index for semantically similar prompts.
func (c *Cache) Lookup(ctx context.Context, namespace, normalizedPrompt, modelID string) (*LookupResult, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "reverb.semantic.lookup")
	defer span.End()
	span.SetAttributes(
		attribute.String("reverb.namespace", namespace),
		attribute.Int("reverb.top_k", c.cfg.TopK),
		attribute.Float64("reverb.threshold", float64(c.cfg.Threshold)),
	)

	// Compute embedding
	emb, err := c.embedder.Embed(ctx, normalizedPrompt)
	if err != nil {
		// Graceful degradation: embedding failure → miss, no error
		span.SetAttributes(attribute.Bool("reverb.hit", false), attribute.Bool("reverb.embedding_failed", true))
		return &LookupResult{Hit: false}, nil
	}

	// Search the vector index
	results, err := c.index.Search(ctx, emb, c.cfg.TopK, c.cfg.Threshold)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	span.SetAttributes(attribute.Int("reverb.candidates", len(results)))
	now := c.clock.Now()

	// Check each candidate
	for _, res := range results {
		entry, err := c.store.Get(ctx, res.ID)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		}
		if entry == nil {
			continue
		}
		// Verify namespace
		if entry.Namespace != namespace {
			continue
		}
		// Verify model ID if scoping is enabled
		if c.cfg.ScopeByModel && modelID != "" && entry.ModelID != modelID {
			continue
		}
		// Verify not expired
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			continue
		}
		span.SetAttributes(
			attribute.Bool("reverb.hit", true),
			attribute.Float64("reverb.similarity", float64(res.Score)),
			attribute.String("reverb.entry_id", entry.ID),
		)
		return &LookupResult{
			Hit:        true,
			Entry:      entry,
			Similarity: res.Score,
		}, nil
	}

	span.SetAttributes(attribute.Bool("reverb.hit", false))
	return &LookupResult{Hit: false}, nil
}
