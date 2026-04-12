package reverb_test

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

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// setupOTel installs an in-memory span exporter as the global TracerProvider
// and restores the previous provider when the test completes.
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

// newOTelClient creates a Reverb client wired to in-memory components.
// The global TracerProvider must be set before calling this so the client's
// internal tracer captures spans.
func newOTelClient(t *testing.T) *reverb.Client {
	t.Helper()
	cfg := reverb.Config{
		DefaultNamespace:    "test-ns",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
	}
	client, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New())
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

// --- span helpers ---

func findSpan(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func spanNames(spans tracetest.SpanStubs) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name
	}
	return names
}

func spanAttrStr(span *tracetest.SpanStub, key string) string {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsString()
		}
	}
	return ""
}

func spanAttrBool(span *tracetest.SpanStub, key string) (bool, bool) {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsBool(), true
		}
	}
	return false, false
}

func spanAttrFloat(span *tracetest.SpanStub, key string) (float64, bool) {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsFloat64(), true
		}
	}
	return 0, false
}

func spanAttrInt(span *tracetest.SpanStub, key string) (int64, bool) {
	for _, a := range span.Attributes {
		if string(a.Key) == key {
			return a.Value.AsInt64(), true
		}
	}
	return 0, false
}

// --- Client-level OTel tests ---

func TestOTel_Lookup_ExactHit(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	// Store an entry first.
	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "How do I reset my password?",
		ModelID:  "gpt-4",
		Response: "Click forgot password.",
	})
	require.NoError(t, err)
	exporter.Reset()

	// Lookup the same prompt — should be an exact hit.
	resp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "How do I reset my password?",
		ModelID: "gpt-4",
	})
	require.NoError(t, err)
	require.True(t, resp.Hit)
	assert.Equal(t, "exact", resp.Tier)

	spans := exporter.GetSpans()

	// reverb.lookup span must exist with correct attributes.
	lookupSpan := findSpan(spans, "reverb.lookup")
	require.NotNil(t, lookupSpan, "expected reverb.lookup span, got: %v", spanNames(spans))
	assert.Equal(t, "test-ns", spanAttrStr(lookupSpan, "reverb.namespace"))

	hit, ok := spanAttrBool(lookupSpan, "reverb.hit")
	require.True(t, ok, "reverb.hit attribute missing")
	assert.True(t, hit)

	sim, ok := spanAttrFloat(lookupSpan, "reverb.similarity")
	require.True(t, ok)
	assert.Equal(t, 1.0, sim)

	// reverb.exact.lookup child span must exist.
	exactSpan := findSpan(spans, "reverb.exact.lookup")
	require.NotNil(t, exactSpan, "expected reverb.exact.lookup span")

	exactHit, ok := spanAttrBool(exactSpan, "reverb.hit")
	require.True(t, ok)
	assert.True(t, exactHit)

	// reverb.store.get_by_hash child span (from memory store).
	hashSpan := findSpan(spans, "reverb.store.get_by_hash")
	require.NotNil(t, hashSpan, "expected reverb.store.get_by_hash span")
	assert.Equal(t, "memory", spanAttrStr(hashSpan, "reverb.store.backend"))

	// No errors on any span.
	assert.Equal(t, codes.Unset, lookupSpan.Status.Code)
	assert.Equal(t, codes.Unset, exactSpan.Status.Code)
}

func TestOTel_Lookup_Miss(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	resp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "Unknown query with no stored entry",
		ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)

	spans := exporter.GetSpans()

	lookupSpan := findSpan(spans, "reverb.lookup")
	require.NotNil(t, lookupSpan)

	hit, ok := spanAttrBool(lookupSpan, "reverb.hit")
	require.True(t, ok)
	assert.False(t, hit)

	sim, ok := spanAttrFloat(lookupSpan, "reverb.similarity")
	require.True(t, ok)
	assert.Equal(t, 0.0, sim)

	// Both exact and semantic lookups should have been tried.
	assert.NotNil(t, findSpan(spans, "reverb.exact.lookup"))
	assert.NotNil(t, findSpan(spans, "reverb.semantic.lookup"))
}

func TestOTel_Store_CreatesSpans(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "Test prompt for store",
		ModelID:  "gpt-4",
		Response: "Test response",
		Sources:  []reverb.SourceRef{{SourceID: "doc:test"}},
	})
	require.NoError(t, err)

	spans := exporter.GetSpans()

	// reverb.store span.
	storeSpan := findSpan(spans, "reverb.store")
	require.NotNil(t, storeSpan, "expected reverb.store span, got: %v", spanNames(spans))
	assert.Equal(t, "test-ns", spanAttrStr(storeSpan, "reverb.namespace"))
	assert.Equal(t, codes.Unset, storeSpan.Status.Code)

	// reverb.store.put child span (from memory store).
	putSpan := findSpan(spans, "reverb.store.put")
	require.NotNil(t, putSpan, "expected reverb.store.put span")
	assert.Equal(t, "memory", spanAttrStr(putSpan, "reverb.store.backend"))
}

func TestOTel_Invalidate_CreatesSpans(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	// Store an entry with a source reference.
	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "What is the refund policy?",
		ModelID:  "gpt-4",
		Response: "Full refund within 30 days.",
		Sources:  []reverb.SourceRef{{SourceID: "doc:refund"}},
	})
	require.NoError(t, err)
	exporter.Reset()

	// Invalidate by source ID.
	count, err := client.Invalidate(ctx, "doc:refund")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	spans := exporter.GetSpans()

	// reverb.invalidate span.
	invSpan := findSpan(spans, "reverb.invalidate")
	require.NotNil(t, invSpan, "expected reverb.invalidate span, got: %v", spanNames(spans))
	assert.Equal(t, "doc:refund", spanAttrStr(invSpan, "reverb.source_id"))
	assert.Equal(t, codes.Unset, invSpan.Status.Code)

	// reverb.lineage.process_event child span.
	lineageSpan := findSpan(spans, "reverb.lineage.process_event")
	require.NotNil(t, lineageSpan, "expected reverb.lineage.process_event span")
	assert.Equal(t, "doc:refund", spanAttrStr(lineageSpan, "reverb.source_id"))

	invCount, ok := spanAttrInt(lineageSpan, "reverb.invalidated_count")
	require.True(t, ok)
	assert.Equal(t, int64(1), invCount)
}

func TestOTel_InvalidateEntry_CreatesSpans(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	entry, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "Test prompt",
		ModelID:  "gpt-4",
		Response: "Test response",
	})
	require.NoError(t, err)
	entryID := entry.ID
	exporter.Reset()

	err = client.InvalidateEntry(ctx, entryID)
	require.NoError(t, err)

	spans := exporter.GetSpans()

	invSpan := findSpan(spans, "reverb.invalidate_entry")
	require.NotNil(t, invSpan, "expected reverb.invalidate_entry span, got: %v", spanNames(spans))
	assert.Equal(t, entryID, spanAttrStr(invSpan, "reverb.entry_id"))
	assert.Equal(t, codes.Unset, invSpan.Status.Code)

	// Store delete should be a child span.
	delSpan := findSpan(spans, "reverb.store.delete")
	require.NotNil(t, delSpan, "expected reverb.store.delete span")
}

func TestOTel_SpanHierarchy_LookupExact(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "Hello",
		ModelID:  "gpt-4",
		Response: "World",
	})
	require.NoError(t, err)
	exporter.Reset()

	_, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "Hello",
		ModelID: "gpt-4",
	})
	require.NoError(t, err)

	spans := exporter.GetSpans()
	lookupSpan := findSpan(spans, "reverb.lookup")
	exactSpan := findSpan(spans, "reverb.exact.lookup")
	require.NotNil(t, lookupSpan)
	require.NotNil(t, exactSpan)

	// All spans in this operation share the same trace ID.
	assert.Equal(t, lookupSpan.SpanContext.TraceID(), exactSpan.SpanContext.TraceID(),
		"exact span should share trace ID with lookup span")

	// The exact span's parent should be the lookup span.
	assert.Equal(t, lookupSpan.SpanContext.SpanID(), exactSpan.Parent.SpanID(),
		"exact span parent should be the lookup span")
}

func TestOTel_SpanHierarchy_Store(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "hierarchy test",
		ModelID:  "gpt-4",
		Response: "response",
	})
	require.NoError(t, err)

	spans := exporter.GetSpans()
	storeSpan := findSpan(spans, "reverb.store")
	putSpan := findSpan(spans, "reverb.store.put")
	require.NotNil(t, storeSpan)
	require.NotNil(t, putSpan)

	// Same trace.
	assert.Equal(t, storeSpan.SpanContext.TraceID(), putSpan.SpanContext.TraceID())

	// store.put is a child of the client store span.
	assert.Equal(t, storeSpan.SpanContext.SpanID(), putSpan.Parent.SpanID(),
		"store.put parent should be the client store span")
}

func TestOTel_SemanticLookup_SpanAttributes(t *testing.T) {
	exporter := setupOTel(t)
	client := newOTelClient(t)
	ctx := context.Background()

	// Store, then lookup a miss to exercise the semantic path.
	_, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   "original prompt",
		ModelID:  "gpt-4",
		Response: "original response",
	})
	require.NoError(t, err)
	exporter.Reset()

	// Different prompt → exact miss → semantic lookup is attempted.
	_, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "completely different prompt",
		ModelID: "gpt-4",
	})
	require.NoError(t, err)

	spans := exporter.GetSpans()

	semSpan := findSpan(spans, "reverb.semantic.lookup")
	require.NotNil(t, semSpan, "expected reverb.semantic.lookup span")
	assert.Equal(t, "test-ns", spanAttrStr(semSpan, "reverb.namespace"))

	topK, ok := spanAttrInt(semSpan, "reverb.top_k")
	require.True(t, ok)
	assert.Equal(t, int64(5), topK)

	threshold, ok := spanAttrFloat(semSpan, "reverb.threshold")
	require.True(t, ok)
	assert.InDelta(t, 0.95, threshold, 0.001)
}
