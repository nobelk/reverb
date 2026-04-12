package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/nobelk/reverb"

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
	ctx, span := t.tracer.Start(ctx, "reverb.lookup")
	span.SetAttributes(
		attribute.String("reverb.namespace", namespace),
	)
	return ctx, span
}

// StartStoreSpan starts a span for a cache store operation.
func (t *Tracer) StartStoreSpan(ctx context.Context, namespace string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.store")
	span.SetAttributes(
		attribute.String("reverb.namespace", namespace),
	)
	return ctx, span
}

// StartInvalidateSpan starts a span for a cache invalidation operation.
func (t *Tracer) StartInvalidateSpan(ctx context.Context, sourceID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.invalidate")
	span.SetAttributes(
		attribute.String("reverb.source_id", sourceID),
	)
	return ctx, span
}

// StartInvalidateEntrySpan starts a span for a single-entry invalidation.
func (t *Tracer) StartInvalidateEntrySpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.invalidate_entry")
	span.SetAttributes(
		attribute.String("reverb.entry_id", entryID),
	)
	return ctx, span
}

// StartEmbedSpan starts a child span for an embedding generation operation.
func (t *Tracer) StartEmbedSpan(ctx context.Context, provider string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.embed")
	span.SetAttributes(
		attribute.String("reverb.embedding.provider", provider),
	)
	return ctx, span
}

// StartVectorSearchSpan starts a child span for a vector similarity search.
func (t *Tracer) StartVectorSearchSpan(ctx context.Context, namespace string, topK int) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.vector_search")
	span.SetAttributes(
		attribute.String("reverb.namespace", namespace),
		attribute.Int("reverb.vector_search.top_k", topK),
	)
	return ctx, span
}

// StartStoreGetSpan starts a child span for a store Get operation.
func (t *Tracer) StartStoreGetSpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.store.get")
	span.SetAttributes(
		attribute.String("reverb.entry_id", entryID),
	)
	return ctx, span
}

// StartStorePutSpan starts a child span for a store Put operation.
func (t *Tracer) StartStorePutSpan(ctx context.Context, entryID, namespace string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.store.put")
	span.SetAttributes(
		attribute.String("reverb.entry_id", entryID),
		attribute.String("reverb.namespace", namespace),
	)
	return ctx, span
}

// StartStoreDeleteSpan starts a child span for a store Delete operation.
func (t *Tracer) StartStoreDeleteSpan(ctx context.Context, entryID string) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.store.delete")
	span.SetAttributes(
		attribute.String("reverb.entry_id", entryID),
	)
	return ctx, span
}

// StartStoreDeleteBatchSpan starts a child span for a store DeleteBatch operation.
func (t *Tracer) StartStoreDeleteBatchSpan(ctx context.Context, count int) (context.Context, trace.Span) {
	ctx, span := t.tracer.Start(ctx, "reverb.store.delete_batch")
	span.SetAttributes(
		attribute.Int("reverb.batch_size", count),
	)
	return ctx, span
}

// SetHitAttributes adds hit/similarity attributes to a span after a lookup completes.
func SetHitAttributes(span trace.Span, hit bool, similarity float64, entryID string) {
	span.SetAttributes(
		attribute.Bool("reverb.hit", hit),
		attribute.Float64("reverb.similarity", similarity),
		attribute.String("reverb.entry_id", entryID),
	)
}

// RecordError records an error on a span and sets the span status to Error.
func RecordError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
