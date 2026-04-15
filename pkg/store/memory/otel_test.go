package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/memory"
)

func setupOTel(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(prev)
	})
	return exporter
}

func findSpan(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func spanAttrStr(span *tracetest.SpanStub, key string) string {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

func spanAttrInt(span *tracetest.SpanStub, key string) int64 {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsInt64()
		}
	}
	return 0
}

func TestOTel_Get_CreatesSpan(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()
	ctx := context.Background()

	_, err := s.Get(ctx, "nonexistent-id")
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.cache.store.get", spans[0].Name)
	assert.Equal(t, "memory", spanAttrStr(&spans[0], "gen_ai.cache.store.backend"))
	assert.Equal(t, "nonexistent-id", spanAttrStr(&spans[0], "gen_ai.cache.entry_id"))
	assert.Equal(t, codes.Unset, spans[0].Status.Code)
}

func TestOTel_GetByHash_CreatesSpan(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()
	ctx := context.Background()

	var hash [32]byte
	_, err := s.GetByHash(ctx, "ns1", hash)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.cache.store.get_by_hash", spans[0].Name)
	assert.Equal(t, "memory", spanAttrStr(&spans[0], "gen_ai.cache.store.backend"))
	assert.Equal(t, "ns1", spanAttrStr(&spans[0], "gen_ai.cache.namespace"))
}

func TestOTel_Put_CreatesSpan(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()
	ctx := context.Background()

	entry := &store.CacheEntry{
		ID:        "entry-1",
		Namespace: "ns1",
		CreatedAt: time.Now(),
	}
	err := s.Put(ctx, entry)
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.cache.store.put", spans[0].Name)
	assert.Equal(t, "memory", spanAttrStr(&spans[0], "gen_ai.cache.store.backend"))
	assert.Equal(t, "entry-1", spanAttrStr(&spans[0], "gen_ai.cache.entry_id"))
	assert.Equal(t, "ns1", spanAttrStr(&spans[0], "gen_ai.cache.namespace"))
}

func TestOTel_Delete_CreatesSpan(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()
	ctx := context.Background()

	// Put then delete
	entry := &store.CacheEntry{
		ID:        "entry-del",
		Namespace: "ns1",
		CreatedAt: time.Now(),
	}
	require.NoError(t, s.Put(ctx, entry))
	exporter.Reset()

	err := s.Delete(ctx, "entry-del")
	require.NoError(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.cache.store.delete", spans[0].Name)
	assert.Equal(t, "memory", spanAttrStr(&spans[0], "gen_ai.cache.store.backend"))
	assert.Equal(t, "entry-del", spanAttrStr(&spans[0], "gen_ai.cache.entry_id"))
}

func TestOTel_DeleteBatch_CreatesSpan(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()
	ctx := context.Background()

	// Put two entries
	for _, id := range []string{"batch-1", "batch-2"} {
		require.NoError(t, s.Put(ctx, &store.CacheEntry{
			ID:        id,
			Namespace: "ns1",
			CreatedAt: time.Now(),
		}))
	}
	exporter.Reset()

	err := s.DeleteBatch(ctx, []string{"batch-1", "batch-2"})
	require.NoError(t, err)

	spans := exporter.GetSpans()
	batchSpan := findSpan(spans, "gen_ai.cache.store.delete_batch")
	require.NotNil(t, batchSpan)
	assert.Equal(t, "memory", spanAttrStr(batchSpan, "gen_ai.cache.store.backend"))
	assert.Equal(t, int64(2), spanAttrInt(batchSpan, "gen_ai.cache.batch_size"))
}

func TestOTel_CancelledContext_RecordsError(t *testing.T) {
	exporter := setupOTel(t)
	s := memory.New()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := s.Get(ctx, "any-id")
	require.Error(t, err)

	spans := exporter.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, "gen_ai.cache.store.get", spans[0].Name)
	assert.Equal(t, codes.Error, spans[0].Status.Code, "cancelled context should set error status")
}
