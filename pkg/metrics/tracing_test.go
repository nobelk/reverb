package metrics_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"
	"github.com/stretchr/testify/assert"

	"github.com/nobelk/reverb/pkg/metrics"
)

func TestTracer_StartLookupSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartLookupSpan(context.Background(), "ns1")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestTracer_StartStoreSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartStoreSpan(context.Background(), "ns1")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestTracer_StartInvalidateSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartInvalidateSpan(context.Background(), "doc:abc")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestTracer_StartInvalidateEntrySpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartInvalidateEntrySpan(context.Background(), "entry-id-123")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestStoreTracer_StartDelete(t *testing.T) {
	tp := noop.NewTracerProvider()
	st := metrics.NewStoreTracerWithProvider(tp, "memory")

	ctx, span := st.StartDelete(context.Background(), "entry-id-123")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestStoreTracer_StartDeleteBatch(t *testing.T) {
	tp := noop.NewTracerProvider()
	st := metrics.NewStoreTracerWithProvider(tp, "memory")

	ctx, span := st.StartDeleteBatch(context.Background(), 5)
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestRecordError(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	_, span := tr.StartLookupSpan(context.Background(), "ns1")
	// Should not panic with noop span.
	metrics.RecordError(span, assert.AnError)
	span.End()
}

func TestTracer_StartEmbedSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartEmbedSpan(context.Background(), "openai")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestTracer_StartVectorSearchSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartVectorSearchSpan(context.Background(), "ns1", 5)
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestStoreTracer_StartGet(t *testing.T) {
	tp := noop.NewTracerProvider()
	st := metrics.NewStoreTracerWithProvider(tp, "redis")

	ctx, span := st.StartGet(context.Background(), "entry-id-123")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestStoreTracer_StartPut(t *testing.T) {
	tp := noop.NewTracerProvider()
	st := metrics.NewStoreTracerWithProvider(tp, "redis")

	ctx, span := st.StartPut(context.Background(), "entry-id-123", "ns1")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestNewStoreTracer_UsesGlobalProvider(t *testing.T) {
	st := metrics.NewStoreTracer("badger")
	assert.NotNil(t, st)
	ctx, span := st.StartGet(context.Background(), "id")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	span.End()
}

func TestSetHitAttributes(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	_, span := tr.StartLookupSpan(context.Background(), "ns1")
	// Should not panic with noop span.
	metrics.SetHitAttributes(span, true, 0.97, "entry-id-456", "exact")
	span.End()
}

func TestNewTracer_UsesGlobalProvider(t *testing.T) {
	// NewTracer() uses otel.GetTracerProvider() which defaults to the noop provider.
	tr := metrics.NewTracer()
	assert.NotNil(t, tr)

	ctx, span := tr.StartLookupSpan(context.Background(), "ns")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	span.End()
}
