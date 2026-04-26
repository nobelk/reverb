package lineage

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/vector"
)

const tracerName = "github.com/nobelk/reverb/pkg/lineage"

// ChangeEvent represents a source document that has changed.
type ChangeEvent struct {
	SourceID    string
	ContentHash [32]byte // zero value means deleted
	Timestamp   time.Time
}

// Invalidator processes source change events and invalidates cache entries.
type Invalidator struct {
	store       store.Store
	vectorIndex vector.Index
	index       *Index
	logger      *slog.Logger
}

// NewInvalidator creates a new invalidation engine.
func NewInvalidator(s store.Store, vi vector.Index, idx *Index, logger *slog.Logger) *Invalidator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Invalidator{
		store:       s,
		vectorIndex: vi,
		index:       idx,
		logger:      logger,
	}
}

// ProcessEvent handles a single change event, invalidating affected cache entries.
// Returns the number of entries invalidated.
func (inv *Invalidator) ProcessEvent(ctx context.Context, event ChangeEvent) (int, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.lineage.process_event")
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.system", "reverb"),
		attribute.String("gen_ai.cache.source_id", event.SourceID),
	)

	entryIDs, err := inv.index.EntriesForSource(ctx, event.SourceID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}

	span.SetAttributes(attribute.Int("gen_ai.cache.linked_entries", len(entryIDs)))

	var toDelete []string
	isDeleted := event.ContentHash == [32]byte{} // zero hash means source deleted

	for _, id := range entryIDs {
		entry, err := inv.store.Get(ctx, id)
		if err != nil {
			inv.logger.Error("failed to load entry for invalidation",
				"entry_id", id, "error", err)
			continue
		}
		if entry == nil {
			continue
		}

		shouldInvalidate := isDeleted
		if !shouldInvalidate {
			for _, src := range entry.SourceHashes {
				if src.SourceID == event.SourceID && src.ContentHash != event.ContentHash {
					shouldInvalidate = true
					break
				}
			}
		}

		if shouldInvalidate {
			toDelete = append(toDelete, id)
		}
	}

	if len(toDelete) == 0 {
		span.SetAttributes(attribute.Int("gen_ai.cache.invalidated_count", 0))
		return 0, nil
	}

	// Delete from vector index
	for _, id := range toDelete {
		if err := inv.vectorIndex.Delete(ctx, id); err != nil {
			inv.logger.Error("failed to delete vector", "entry_id", id, "error", err)
		}
	}

	// Delete from store in batch
	if err := inv.store.DeleteBatch(ctx, toDelete); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}

	span.SetAttributes(attribute.Int("gen_ai.cache.invalidated_count", len(toDelete)))
	inv.logger.Info("invalidated entries",
		"source_id", event.SourceID,
		"count", len(toDelete))

	return len(toDelete), nil
}

