package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/store"
)

const tracerName = "github.com/nobelk/reverb/pkg/store/memory"

// Store is an in-memory store. All maps are protected by a single RWMutex
// for simplicity and correctness.
type Store struct {
	mu       sync.RWMutex
	entries  map[string]*store.CacheEntry          // by ID
	byHash   map[string]*store.CacheEntry          // namespace:hash → entry
	lineage  map[string]map[string]struct{}         // sourceID → set of entryIDs
	hitCount map[string]*atomic.Int64               // entryID → hit count (lock-free)
	hitTime  map[string]*atomic.Int64               // entryID → last hit unix nano
}

// New creates a new in-memory store.
func New() *Store {
	return &Store{
		entries:  make(map[string]*store.CacheEntry),
		byHash:   make(map[string]*store.CacheEntry),
		lineage:  make(map[string]map[string]struct{}),
		hitCount: make(map[string]*atomic.Int64),
		hitTime:  make(map[string]*atomic.Int64),
	}
}

func hashKey(namespace string, hash [32]byte) string {
	return namespace + ":" + string(hash[:])
}

func (s *Store) Get(ctx context.Context, id string) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "memory"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	s.mu.RLock()
	entry, ok := s.entries[id]
	if !ok {
		s.mu.RUnlock()
		return nil, nil
	}
	cp := copyEntry(entry)
	counter := s.hitCount[id]
	ts := s.hitTime[id]
	s.mu.RUnlock()
	// Apply atomic hit count/time
	if counter != nil {
		cp.HitCount = counter.Load()
	}
	if ts != nil {
		if v := ts.Load(); v != 0 {
			cp.LastHitAt = time.Unix(0, v)
		}
	}
	return cp, nil
}

func (s *Store) GetByHash(ctx context.Context, namespace string, hash [32]byte) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get_by_hash")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "memory"), attribute.String("gen_ai.cache.namespace", namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	key := hashKey(namespace, hash)
	s.mu.RLock()
	entry, ok := s.byHash[key]
	if !ok {
		s.mu.RUnlock()
		return nil, nil
	}
	cp := copyEntry(entry)
	counter := s.hitCount[entry.ID]
	ts := s.hitTime[entry.ID]
	s.mu.RUnlock()
	// Apply atomic hit count/time
	if counter != nil {
		cp.HitCount = counter.Load()
	}
	if ts != nil {
		if v := ts.Load(); v != 0 {
			cp.LastHitAt = time.Unix(0, v)
		}
	}
	return cp, nil
}

func (s *Store) Put(ctx context.Context, entry *store.CacheEntry) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.put")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "memory"), attribute.String("gen_ai.cache.entry_id", entry.ID), attribute.String("gen_ai.cache.namespace", entry.Namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	copied := copyEntry(entry)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove old entry's indices if overwriting
	if old, ok := s.entries[entry.ID]; ok {
		oldKey := hashKey(old.Namespace, old.PromptHash)
		delete(s.byHash, oldKey)
		s.removeLineageLocked(old)
	}

	s.entries[entry.ID] = copied
	key := hashKey(entry.Namespace, entry.PromptHash)
	s.byHash[key] = copied

	// Initialize atomic counters
	s.hitCount[entry.ID] = &atomic.Int64{}
	s.hitTime[entry.ID] = &atomic.Int64{}

	// Update lineage index
	for _, src := range copied.SourceHashes {
		if s.lineage[src.SourceID] == nil {
			s.lineage[src.SourceID] = make(map[string]struct{})
		}
		s.lineage[src.SourceID][entry.ID] = struct{}{}
	}

	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "memory"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[id]
	if !ok {
		return nil
	}

	key := hashKey(entry.Namespace, entry.PromptHash)
	delete(s.byHash, key)
	delete(s.entries, id)
	delete(s.hitCount, id)
	delete(s.hitTime, id)
	s.removeLineageLocked(entry)

	return nil
}

func (s *Store) DeleteBatch(ctx context.Context, ids []string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete_batch")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "memory"), attribute.Int("gen_ai.cache.batch_size", len(ids)))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		entry, ok := s.entries[id]
		if !ok {
			continue
		}
		key := hashKey(entry.Namespace, entry.PromptHash)
		delete(s.byHash, key)
		delete(s.entries, id)
		delete(s.hitCount, id)
		delete(s.hitTime, id)
		s.removeLineageLocked(entry)
	}
	return nil
}

func (s *Store) ListBySource(ctx context.Context, sourceID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	entrySet, ok := s.lineage[sourceID]
	if !ok {
		return nil, nil
	}
	ids := make([]string, 0, len(entrySet))
	for id := range entrySet {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *Store) IncrementHit(_ context.Context, id string) error {
	s.mu.RLock()
	counter, cOk := s.hitCount[id]
	ts, tOk := s.hitTime[id]
	s.mu.RUnlock()
	if cOk {
		counter.Add(1)
	}
	if tOk {
		ts.Store(time.Now().UnixNano())
	}
	return nil
}

func (s *Store) Scan(ctx context.Context, namespace string, fn func(entry *store.CacheEntry) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	// Collect entries under read lock
	var matched []*store.CacheEntry
	for _, entry := range s.entries {
		if entry.Namespace == namespace {
			matched = append(matched, copyEntry(entry))
		}
	}
	s.mu.RUnlock()

	for _, entry := range matched {
		if !fn(entry) {
			break
		}
	}
	return nil
}

func (s *Store) Stats(ctx context.Context) (*store.StoreStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	nsSet := make(map[string]struct{})
	for _, entry := range s.entries {
		nsSet[entry.Namespace] = struct{}{}
	}
	namespaces := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		namespaces = append(namespaces, ns)
	}
	return &store.StoreStats{
		TotalEntries: int64(len(s.entries)),
		Namespaces:   namespaces,
	}, nil
}

func (s *Store) Close() error {
	return nil
}

// removeLineageLocked removes an entry from the lineage index. Must be called with s.mu held.
func (s *Store) removeLineageLocked(entry *store.CacheEntry) {
	for _, src := range entry.SourceHashes {
		if set, ok := s.lineage[src.SourceID]; ok {
			delete(set, entry.ID)
			if len(set) == 0 {
				delete(s.lineage, src.SourceID)
			}
		}
	}
}

func copyEntry(e *store.CacheEntry) *store.CacheEntry {
	cp := *e
	if e.Embedding != nil {
		cp.Embedding = make([]float32, len(e.Embedding))
		copy(cp.Embedding, e.Embedding)
	}
	if e.SourceHashes != nil {
		cp.SourceHashes = make([]store.SourceRef, len(e.SourceHashes))
		copy(cp.SourceHashes, e.SourceHashes)
	}
	if e.ResponseMeta != nil {
		cp.ResponseMeta = make(map[string]string, len(e.ResponseMeta))
		for k, v := range e.ResponseMeta {
			cp.ResponseMeta[k] = v
		}
	}
	return &cp
}
