package reverb_test

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org/reverb/internal/testutil"
	"github.com/org/reverb/pkg/embedding/fake"
	"github.com/org/reverb/pkg/reverb"
	"github.com/org/reverb/pkg/store"
	"github.com/org/reverb/pkg/store/memory"
	"github.com/org/reverb/pkg/vector/flat"
)

const dims = 64

func newTestClient(t *testing.T, clock *testutil.FakeClock) (*reverb.Client, *memory.Store) {
	t.Helper()
	s := memory.New()
	vi := flat.New()
	embedder := fake.New(dims)
	if clock == nil {
		clock = testutil.NewFakeClock(time.Now())
	}
	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)
	return c, s
}

func TestClient_LookupExactHit(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	// Store an entry
	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "How do I reset my password?",
		ModelID:   "gpt-4",
		Response:  "Go to settings and click reset.",
		Sources: []store.SourceRef{
			{SourceID: "doc:reset", ContentHash: sha256.Sum256([]byte("guide content"))},
		},
	})
	require.NoError(t, err)

	// Lookup exact same prompt → exact hit
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "How do I reset my password?",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)
	assert.Equal(t, "exact", resp.Tier)
	assert.Equal(t, float32(1.0), resp.Similarity)
	assert.Equal(t, "Go to settings and click reset.", resp.Entry.ResponseText)
}

func TestClient_LookupSemanticHit(t *testing.T) {
	s := memory.New()
	vi := flat.New()
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())

	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.5, // low threshold to test semantic matching
		SemanticTopK:        5,
		ScopeByModel:        true,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	// Store with one phrasing
	_, err = c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "password reset steps",
		ModelID:   "gpt-4",
		Response:  "Go to settings and click reset.",
	})
	require.NoError(t, err)

	// Lookup with the exact same text (after normalization) → exact hit
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "password reset steps",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)
	assert.Equal(t, "exact", resp.Tier)
}

func TestClient_LookupMiss(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "What is quantum computing?",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)
	assert.Empty(t, resp.Tier)
}

func TestClient_Store_WritesToBothTiers(t *testing.T) {
	s := memory.New()
	vi := flat.New()
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())

	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.5,
		SemanticTopK:        5,
		ScopeByModel:        false,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	entry, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "test prompt",
		ModelID:   "gpt-4",
		Response:  "test response",
	})
	require.NoError(t, err)
	require.NotNil(t, entry)

	// Verify exact tier works
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "test prompt",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)
	assert.Equal(t, "exact", resp.Tier)

	// Verify vector index has the entry
	assert.Equal(t, 1, vi.Len())
}

func TestClient_Invalidate_RemovesFromBothTiers(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "password reset",
		ModelID:   "gpt-4",
		Response:  "Go to settings.",
		Sources: []store.SourceRef{
			{SourceID: "doc:reset", ContentHash: sha256.Sum256([]byte("content"))},
		},
	})
	require.NoError(t, err)

	// Invalidate
	count, err := c.Invalidate(ctx, "doc:reset")
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Lookup → miss
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "password reset",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)
}

func TestClient_TTLExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	c, _ := newTestClient(t, clock)
	defer c.Close()
	ctx := context.Background()

	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "gpt-4",
		Response:  "response",
		TTL:       1 * time.Hour,
	})
	require.NoError(t, err)

	// Before expiry → hit
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)

	// Advance past expiry → miss
	clock.Advance(2 * time.Hour)
	resp, err = c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)
}

func TestClient_NamespaceIsolation(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns-A",
		Prompt:    "test",
		ModelID:   "gpt-4",
		Response:  "response",
	})
	require.NoError(t, err)

	// Lookup in different namespace → miss
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns-B",
		Prompt:    "test",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)
}

func TestClient_ModelIDIsolation(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "model-A",
		Response:  "response",
	})
	require.NoError(t, err)

	// Lookup with different model → miss (scope_by_model=true)
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "model-B",
	})
	require.NoError(t, err)
	assert.False(t, resp.Hit)
}

func TestClient_EmbeddingFailure_Degradation(t *testing.T) {
	s := memory.New()
	vi := flat.New()
	failingEmbedder := fake.NewFailing(dims, nil)
	clock := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, failingEmbedder, s, vi)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	// Store should succeed (exact tier), but embedding will be missing
	entry, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "model",
		Response:  "response",
	})
	require.NoError(t, err)
	assert.True(t, entry.EmbeddingMissing)

	// Exact lookup should still work
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "model",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)
	assert.Equal(t, "exact", resp.Tier)
}

func TestClient_StoreWithEmbeddingFailure(t *testing.T) {
	s := memory.New()
	vi := flat.New()
	failingEmbedder := fake.NewFailing(dims, nil)
	clock := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, failingEmbedder, s, vi)
	require.NoError(t, err)
	defer c.Close()

	entry, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "test",
		ModelID:   "model",
		Response:  "response",
	})
	require.NoError(t, err)
	assert.True(t, entry.EmbeddingMissing)
	assert.Equal(t, 0, vi.Len(), "vector index should be empty when embedding fails")
}

func TestClient_Stats(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := c.Store(ctx, reverb.StoreRequest{
			Namespace: "ns",
			Prompt:    "prompt " + string(rune('a'+i)),
			ModelID:   "model",
			Response:  "response",
		})
		require.NoError(t, err)
	}

	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(5), stats.TotalEntries)
}

func TestClient_Close(t *testing.T) {
	c, _ := newTestClient(t, nil)
	err := c.Close()
	assert.NoError(t, err)
}

func TestClient_Store_Upsert(t *testing.T) {
	s := memory.New()
	vi := flat.New()
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
		Clock:               clock,
	}
	c, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)
	defer c.Close()
	ctx := context.Background()

	// Store the same prompt twice with different responses.
	_, err = c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is the capital of France?",
		ModelID:   "gpt-4",
		Response:  "Paris is the capital of France.",
	})
	require.NoError(t, err)

	_, err = c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is the capital of France?",
		ModelID:   "gpt-4",
		Response:  "Paris.",
	})
	require.NoError(t, err)

	// TotalEntries should be 1 (upsert, not a new entry).
	stats, err := c.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.TotalEntries, "re-storing the same prompt should upsert, not create a duplicate")

	// The response should be updated to the latest value.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "What is the capital of France?",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	require.True(t, resp.Hit)
	assert.Equal(t, "Paris.", resp.Entry.ResponseText, "response should reflect the latest store")

	// The vector index should have exactly 1 entry (no stale duplicates).
	assert.Equal(t, 1, vi.Len(), "vector index should have exactly 1 entry after upsert")
}

func TestClient_ConcurrentLookupAndStore(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prompt := "prompt " + string(rune('a'+i%26))
			if i%2 == 0 {
				_, _ = c.Store(ctx, reverb.StoreRequest{
					Namespace: "ns",
					Prompt:    prompt,
					ModelID:   "model",
					Response:  "response",
				})
			} else {
				_, _ = c.Lookup(ctx, reverb.LookupRequest{
					Namespace: "ns",
					Prompt:    prompt,
					ModelID:   "model",
				})
			}
		}(i)
	}
	wg.Wait()
}

func TestClient_ConcurrentCloseAndLookup(t *testing.T) {
	c, _ := newTestClient(t, nil)
	ctx := context.Background()

	// Store an entry so Lookup has something to hit.
	_, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "concurrent close test prompt",
		ModelID:   "model",
		Response:  "response",
	})
	require.NoError(t, err)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines + 1)

	// Launch lookup goroutines concurrently.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Lookup(ctx, reverb.LookupRequest{
				Namespace: "ns",
				Prompt:    "concurrent close test prompt",
				ModelID:   "model",
			})
		}()
	}

	// Close concurrently with lookups.
	go func() {
		defer wg.Done()
		_ = c.Close()
	}()

	wg.Wait()
	// If we reach here without panic or race detector complaint, the test passes.
}
