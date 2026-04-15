package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/nobelk/reverb"

// Common GenAI semantic convention attributes.
var attrSystem = attribute.String("gen_ai.system", "reverb")

// Tracer wraps an OpenTelemetry tracer with Reverb-specific span helpers.
type Tracer struct {
	tracer trace.Tracer
}

// NewTracer creates a Tracer using the global OTel tracer provider.
func NewTracer() *Tracer {
	return &Tracer{tracer: otel.Tracer(tracerName)}
}

// NewTracerWithProvider creates a Tracer using the supplied provider.
func NewTracerWithProvider(tp trace.TracerProvider) *Tracer {
	return &Tracer{tracer: tp.Tracer(tracerName)}
}

// StartLookupSpan starts a span for a cache lookup operation.
// Callers must call the returned span.End() when the operation completes.
func (t *Tracer) StartLookupSpan(ctx context.Context, namespace string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.lookup")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "lookup"),
		attribute.String("gen_ai.cache.namespace", namespace),
	)
	return ctx, span
}

// StartStoreSpan starts a span for a cache store operation.
func (t *Tracer) StartStoreSpan(ctx context.Context, namespace string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.store")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "store"),
		attribute.String("gen_ai.cache.namespace", namespace),
	)
	return ctx, span
}

// StartInvalidateSpan starts a span for a cache invalidation operation.
func (t *Tracer) StartInvalidateSpan(ctx context.Context, sourceID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.invalidate")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "invalidate"),
		attribute.String("gen_ai.cache.source_id", sourceID),
	)
	return ctx, span
}

// StartInvalidateEntrySpan starts a span for a single-entry invalidation.
func (t *Tracer) StartInvalidateEntrySpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.invalidate_entry")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "invalidate_entry"),
		attribute.String("gen_ai.cache.entry_id", entryID),
	)
	return ctx, span
}

// StartEmbedSpan starts a child span for an embedding generation operation.
func (t *Tracer) StartEmbedSpan(ctx context.Context, provider string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.embed")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "embed"),
		attribute.String("gen_ai.request.embedding.provider", provider),
	)
	return ctx, span
}

// StartVectorSearchSpan starts a child span for a vector similarity search.
func (t *Tracer) StartVectorSearchSpan(ctx context.Context, namespace string, topK int) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.vector_search")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.operation.name", "vector_search"),
		attribute.String("gen_ai.cache.namespace", namespace),
		attribute.Int("gen_ai.cache.vector_search.top_k", topK),
	)
	return ctx, span
}

// StartStoreGetSpan starts a child span for a store Get operation.
func (t *Tracer) StartStoreGetSpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.store.get")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.cache.entry_id", entryID),
	)
	return ctx, span
}

// StartStorePutSpan starts a child span for a store Put operation.
func (t *Tracer) StartStorePutSpan(ctx context.Context, entryID, namespace string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.store.put")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.cache.entry_id", entryID),
		attribute.String("gen_ai.cache.namespace", namespace),
	)
	return ctx, span
}

// StartStoreDeleteSpan starts a child span for a store Delete operation.
func (t *Tracer) StartStoreDeleteSpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.store.delete")
	span.SetAttributes(
		attrSystem,
		attribute.String("gen_ai.cache.entry_id", entryID),
	)
	return ctx, span
}

// StartStoreDeleteBatchSpan starts a child span for a store DeleteBatch operation.
func (t *Tracer) StartStoreDeleteBatchSpan(ctx context.Context, count int) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "gen_ai.cache.store.delete_batch")
	span.SetAttributes(
		attrSystem,
		attribute.Int("gen_ai.cache.batch_size", count),
	)
	return ctx, span
}

// SetHitAttributes adds hit/similarity/tier attributes to a span after a lookup completes.
func SetHitAttributes(span trace.Span, hit bool, similarity float64, entryID string, tier string) {
	span.SetAttributes(
		attribute.Bool("gen_ai.cache.hit", hit),
		attribute.Float64("gen_ai.cache.similarity", similarity),
		attribute.String("gen_ai.cache.entry_id", entryID),
		attribute.String("gen_ai.cache.tier", tier),
	)
}

// RecordError records an error on a span and sets the span status to Error.
func RecordError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
