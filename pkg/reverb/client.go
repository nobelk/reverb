package reverb

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/org/reverb/internal/hashutil"
	"github.com/org/reverb/pkg/cache/exact"
	"github.com/org/reverb/pkg/cache/semantic"
	"github.com/org/reverb/pkg/cdc"
	"github.com/org/reverb/pkg/embedding"
	"github.com/org/reverb/pkg/lineage"
	"github.com/org/reverb/pkg/metrics"
	"github.com/org/reverb/pkg/normalize"
	"github.com/org/reverb/pkg/store"
	"github.com/org/reverb/pkg/vector"
)

// SourceRef records the identity and content hash of a source document.
type SourceRef = store.SourceRef

// CacheEntry is the atomic unit stored by Reverb.
type CacheEntry = store.CacheEntry

// LookupRequest holds the parameters for a cache lookup.
type LookupRequest struct {
	Namespace string
	Prompt    string
	ModelID   string
}

// LookupResponse holds the result of a cache lookup.
type LookupResponse struct {
	Hit        bool
	Tier       string  // "exact" | "semantic" | ""
	Similarity float32 // 1.0 for exact, 0.0–1.0 for semantic
	Entry      *CacheEntry
}

// StoreRequest holds the parameters for storing a cache entry.
type StoreRequest struct {
	Namespace    string
	Prompt       string
	ModelID      string
	Response     string
	ResponseMeta map[string]string
	Sources      []SourceRef
	TTL          time.Duration
}

// Stats holds cache statistics.
type Stats struct {
	TotalEntries       int64
	Namespaces         []string
	ExactHitsTotal     int64
	SemanticHitsTotal  int64
	MissesTotal        int64
	InvalidationsTotal int64
	HitRate            float64
}

// Client is the primary entry point for Reverb.
// It is safe for concurrent use.
type Client struct {
	cfg          Config
	embedder     embedding.Provider
	exactTier    *exact.Cache
	semanticTier *semantic.Cache
	store        store.Store
	vectorIndex  vector.Index
	invalidator  *lineage.Invalidator
	lineageIdx   *lineage.Index
	clock        Clock
	logger       *slog.Logger
	collector    *metrics.Collector
	cdcListener  cdc.Listener

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new Reverb client with the given configuration and pre-built dependencies.
// Functional options (WithClock, WithLogger, WithCDCListener, WithMetricsCollector) may
// be provided to override defaults.
func New(cfg Config, embedder embedding.Provider, s store.Store, vi vector.Index, opts ...Option) (*Client, error) {
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	logger := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())

	c := &Client{
		cfg:         cfg,
		embedder:    embedder,
		store:       s,
		vectorIndex: vi,
		clock:       cfg.Clock,
		logger:      logger,
		collector:   metrics.NewCollector(),
		ctx:         ctx,
		cancel:      cancel,
	}

	// Apply functional options FIRST (may override clock, logger, collector, cdcListener).
	for _, opt := range opts {
		opt(c)
	}

	// Now construct sub-caches using the potentially-overridden clock.
	lineageIdx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, lineageIdx, c.logger)

	exactCache := exact.New(s, c.clock)
	semanticCache := semantic.New(embedder, vi, s, semantic.Config{
		Threshold:    cfg.SimilarityThreshold,
		TopK:         cfg.SemanticTopK,
		ScopeByModel: cfg.ScopeByModel,
	}, c.clock)

	c.exactTier = exactCache
	c.semanticTier = semanticCache
	c.invalidator = inv
	c.lineageIdx = lineageIdx

	c.wg.Add(1)
	go c.expiryReaper()

	c.wg.Add(1)
	go c.metricsUpdater()

	// If a CDC listener was provided via WithCDCListener, start the invalidation
	// loop and the listener goroutine. Skip both when no CDC listener is configured.
	if c.cdcListener != nil {
		eventCh := make(chan cdc.ChangeEvent, 256)

		c.wg.Add(1)
		go c.invalidationLoop(eventCh)

		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			if err := c.cdcListener.Start(c.ctx, eventCh); err != nil && c.ctx.Err() == nil {
				c.logger.Error("CDC listener exited", "listener", c.cdcListener.Name(), "error", err)
			}
		}()
	}

	return c, nil
}

// invalidationLoop reads CDC change events from ch and processes them in batches.
// It accumulates up to 100 events or 500 ms before flushing.
func (c *Client) invalidationLoop(ch <-chan cdc.ChangeEvent) {
	defer c.wg.Done()

	const batchSize = 100
	const flushInterval = 500 * time.Millisecond

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var pending []cdc.ChangeEvent

	flush := func() {
		for _, ev := range pending {
			lineageEv := lineage.ChangeEvent{
				SourceID:    ev.SourceID,
				ContentHash: ev.ContentHash,
				Timestamp:   ev.Timestamp,
			}
			n, err := c.invalidator.ProcessEvent(c.ctx, lineageEv)
			if err != nil {
				c.logger.Error("invalidation failed", "source_id", ev.SourceID, "error", err)
				continue
			}
			c.collector.Invalidations.Add(int64(n))
		}
		pending = pending[:0]
	}

	for {
		select {
		case <-c.ctx.Done():
			// Drain remaining events.
			for {
				select {
				case ev, ok := <-ch:
					if !ok {
						return
					}
					pending = append(pending, ev)
				default:
					flush()
					return
				}
			}
		case ev, ok := <-ch:
			if !ok {
				flush()
				return
			}
			pending = append(pending, ev)
			if len(pending) >= batchSize {
				flush()
			}
		case <-ticker.C:
			if len(pending) > 0 {
				flush()
			}
		}
	}
}

// expiryReaper runs every 5 minutes and deletes expired entries from the store
// and vector index.
func (c *Client) expiryReaper() {
	defer c.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.reapExpired()
		}
	}
}

func (c *Client) reapExpired() {
	storeStats, err := c.store.Stats(c.ctx)
	if err != nil {
		c.logger.Error("reaper: stats failed", "error", err)
		return
	}

	now := c.clock.Now()
	for _, ns := range storeStats.Namespaces {
		var expired []string
		err := c.store.Scan(c.ctx, ns, func(entry *store.CacheEntry) bool {
			if !entry.ExpiresAt.IsZero() && entry.ExpiresAt.Before(now) {
				expired = append(expired, entry.ID)
			}
			return true
		})
		if err != nil {
			c.logger.Error("reaper: scan failed", "namespace", ns, "error", err)
			continue
		}
		if len(expired) == 0 {
			continue
		}
		for _, id := range expired {
			if err := c.vectorIndex.Delete(c.ctx, id); err != nil {
				c.logger.Error("reaper: vector delete failed", "entry_id", id, "error", err)
			}
		}
		if err := c.store.DeleteBatch(c.ctx, expired); err != nil {
			c.logger.Error("reaper: store delete failed", "namespace", ns, "error", err)
		} else {
			c.logger.Info("reaper: deleted expired entries", "namespace", ns, "count", len(expired))
		}
	}
}

// metricsUpdater recomputes the rolling hit rate every 60 seconds.
func (c *Client) metricsUpdater() {
	defer c.wg.Done()

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// The hit rate is derived on demand from the atomic counters; nothing
			// extra needs to be done here beyond logging it for visibility.
			snap := c.collector.Snapshot()
			c.logger.Debug("metrics snapshot",
				"exact_hits", snap.ExactHits,
				"semantic_hits", snap.SemanticHits,
				"misses", snap.Misses,
				"hit_rate", snap.HitRate(),
			)
		}
	}
}

// Lookup checks the cache for a matching response.
func (c *Client) Lookup(ctx context.Context, req LookupRequest) (*LookupResponse, error) {
	ns := req.Namespace
	if ns == "" {
		ns = c.cfg.DefaultNamespace
	}

	normalized := normalize.Normalize(req.Prompt)
	modelID := req.ModelID

	// Tier 1: Exact match
	hash := hashutil.PromptHash(ns, normalized, modelID)
	exactResult, err := c.exactTier.Lookup(ctx, ns, hash)
	if err != nil {
		return nil, err
	}
	if exactResult.Hit {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			_ = c.store.IncrementHit(context.Background(), exactResult.Entry.ID)
		}()
		c.collector.ExactHits.Add(1)
		c.logger.Info("cache hit",
			"tier", "exact",
			"namespace", ns,
			"entry_id", exactResult.Entry.ID)
		return &LookupResponse{
			Hit:        true,
			Tier:       "exact",
			Similarity: 1.0,
			Entry:      exactResult.Entry,
		}, nil
	}

	// Tier 2: Semantic match
	semanticResult, err := c.semanticTier.Lookup(ctx, ns, normalized, modelID)
	if err != nil {
		return nil, err
	}
	if semanticResult.Hit {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			_ = c.store.IncrementHit(context.Background(), semanticResult.Entry.ID)
		}()
		c.collector.SemanticHits.Add(1)
		c.logger.Info("cache hit",
			"tier", "semantic",
			"namespace", ns,
			"similarity", semanticResult.Similarity,
			"entry_id", semanticResult.Entry.ID)
		return &LookupResponse{
			Hit:        true,
			Tier:       "semantic",
			Similarity: semanticResult.Similarity,
			Entry:      semanticResult.Entry,
		}, nil
	}

	// Miss
	c.collector.Misses.Add(1)
	return &LookupResponse{Hit: false}, nil
}

// Store writes a new cache entry.
func (c *Client) Store(ctx context.Context, req StoreRequest) (*CacheEntry, error) {
	ns := req.Namespace
	if ns == "" {
		ns = c.cfg.DefaultNamespace
	}

	normalized := normalize.Normalize(req.Prompt)
	hash := hashutil.PromptHash(ns, normalized, req.ModelID)

	ttl := req.TTL
	if ttl == 0 {
		ttl = c.cfg.DefaultTTL
	}

	now := c.clock.Now()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	// Compute embedding
	var emb []float32
	var embeddingMissing bool
	embResult, err := c.embedder.Embed(ctx, normalized)
	if err != nil {
		c.logger.Warn("embedding failed during store, storing in exact tier only",
			"error", err)
		embeddingMissing = true
		c.collector.EmbeddingErrors.Add(1)
	} else {
		emb = embResult
	}

	entry := &CacheEntry{
		ID:               uuid.New().String(),
		CreatedAt:        now,
		ExpiresAt:        expiresAt,
		PromptHash:       hash,
		PromptText:       req.Prompt,
		Embedding:        emb,
		ModelID:          req.ModelID,
		Namespace:        ns,
		ResponseText:     req.Response,
		ResponseMeta:     req.ResponseMeta,
		SourceHashes:     req.Sources,
		EmbeddingMissing: embeddingMissing,
	}

	if err := c.store.Put(ctx, entry); err != nil {
		return nil, err
	}

	c.collector.Stores.Add(1)

	// Add to vector index if embedding succeeded
	if !embeddingMissing {
		if err := c.vectorIndex.Add(ctx, entry.ID, emb); err != nil {
			c.logger.Error("failed to add vector to index", "error", err)
		}
	}

	c.logger.Info("stored cache entry",
		"entry_id", entry.ID,
		"namespace", ns,
		"sources_count", len(req.Sources))

	return entry, nil
}

// Invalidate manually invalidates all cache entries that depend on the given source ID.
func (c *Client) Invalidate(ctx context.Context, sourceID string) (int, error) {
	count, err := c.invalidator.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    sourceID,
		ContentHash: [32]byte{}, // zero → treat as deletion
		Timestamp:   c.clock.Now(),
	})
	if err != nil {
		return 0, err
	}
	c.collector.Invalidations.Add(int64(count))
	return count, nil
}

// InvalidateEntry deletes a single cache entry by ID.
func (c *Client) InvalidateEntry(ctx context.Context, entryID string) error {
	// Remove from vector index
	if err := c.vectorIndex.Delete(ctx, entryID); err != nil {
		c.logger.Error("failed to delete vector", "entry_id", entryID, "error", err)
	}
	return c.store.Delete(ctx, entryID)
}

// Stats returns cache statistics.
func (c *Client) Stats(ctx context.Context) (*Stats, error) {
	storeStats, err := c.store.Stats(ctx)
	if err != nil {
		return nil, err
	}
	snap := c.collector.Snapshot()
	return &Stats{
		TotalEntries:       storeStats.TotalEntries,
		Namespaces:         storeStats.Namespaces,
		ExactHitsTotal:     snap.ExactHits,
		SemanticHitsTotal:  snap.SemanticHits,
		MissesTotal:        snap.Misses,
		InvalidationsTotal: snap.Invalidations,
		HitRate:            snap.HitRate(),
	}, nil
}

// Close shuts down the client and releases resources.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	return c.store.Close()
}
