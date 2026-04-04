package metrics_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace/noop"
	"github.com/stretchr/testify/assert"

	"github.com/org/reverb/pkg/metrics"
)

func TestTracer_StartLookupSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartLookupSpan(context.Background(), "ns1", "exact")
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

	ctx, span := tr.StartInvalidateSpan(context.Background(), "ns1", "doc:abc", 3)
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
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

func TestTracer_StartStoreGetSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartStoreGetSpan(context.Background(), "entry-id-123")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestTracer_StartStorePutSpan(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	ctx, span := tr.StartStorePutSpan(context.Background(), "entry-id-123", "ns1")
	assert.NotNil(t, span)
	assert.NotNil(t, ctx)
	span.End()
}

func TestSetHitAttributes(t *testing.T) {
	tp := noop.NewTracerProvider()
	tr := metrics.NewTracerWithProvider(tp)

	_, span := tr.StartLookupSpan(context.Background(), "ns1", "semantic")
	// Should not panic with noop span.
	metrics.SetHitAttributes(span, true, 0.97, "entry-id-456")
	span.End()
}

func TestNewTracer_UsesGlobalProvider(t *testing.T) {
	// NewTracer() uses otel.GetTracerProvider() which defaults to the noop provider.
	tr := metrics.NewTracer()
	assert.NotNil(t, tr)

	ctx, span := tr.StartLookupSpan(context.Background(), "ns", "exact")
	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	span.End()
}
