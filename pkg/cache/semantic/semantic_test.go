package semantic_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/cache/semantic"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

const dims = 64

func setupCache(t *testing.T, cfg semantic.Config, clock *testutil.FakeClock) (*semantic.Cache, *memory.Store, *fake.Provider) {
	t.Helper()
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	if clock == nil {
		clock = testutil.NewFakeClock(time.Now())
	}
	c := semantic.New(embedder, idx, s, cfg, clock)
	return c, s, embedder
}

func storeEntry(t *testing.T, ctx context.Context, s *memory.Store, idx *flat.Index, embedder *fake.Provider, namespace, prompt, response, modelID string, expiresAt time.Time) {
	t.Helper()
	emb, _ := embedder.Embed(ctx, prompt)
	entry := testutil.NewEntry().
		WithNamespace(namespace).
		WithPrompt(prompt).
		WithResponse(response).
		WithModelID(modelID).
		WithEmbedding(emb).
		Build()
	if !expiresAt.IsZero() {
		entry.ExpiresAt = expiresAt
	}
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb))
}

func TestSemantic_ExactVectorMatch(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.95}, clock)
	ctx := context.Background()

	prompt := "how do i reset my password"
	emb, _ := embedder.Embed(ctx, prompt)
	entry := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt(prompt).
		WithResponse("go to settings").
		WithModelID("model").
		WithEmbedding(emb).
		Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb))

	result, err := c.Lookup(ctx, "ns", prompt, "model")
	require.NoError(t, err)
	assert.True(t, result.Hit)
	assert.InDelta(t, 1.0, result.Similarity, 0.01)
	assert.Equal(t, "go to settings", result.Entry.ResponseText)
}

func TestSemantic_BelowThreshold(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.99}, clock)
	ctx := context.Background()

	// Store one prompt
	prompt1 := "how do i reset my password"
	emb1, _ := embedder.Embed(ctx, prompt1)
	entry := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt(prompt1).
		WithResponse("go to settings").
		WithModelID("model").
		WithEmbedding(emb1).
		Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb1))

	// Search with a completely different prompt
	result, err := c.Lookup(ctx, "ns", "what is the weather today in paris france europe", "model")
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestSemantic_NamespaceFilter(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.5}, clock)
	ctx := context.Background()

	prompt := "hello world"
	emb, _ := embedder.Embed(ctx, prompt)
	entry := testutil.NewEntry().
		WithNamespace("ns-A").
		WithPrompt(prompt).
		WithResponse("response").
		WithModelID("model").
		WithEmbedding(emb).
		Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb))

	// Lookup in wrong namespace → miss
	result, err := c.Lookup(ctx, "ns-B", prompt, "model")
	require.NoError(t, err)
	assert.False(t, result.Hit)

	// Lookup in correct namespace → hit
	result, err = c.Lookup(ctx, "ns-A", prompt, "model")
	require.NoError(t, err)
	assert.True(t, result.Hit)
}

func TestSemantic_ModelFilter(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.5, ScopeByModel: true}, clock)
	ctx := context.Background()

	prompt := "hello world"
	emb, _ := embedder.Embed(ctx, prompt)
	entry := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt(prompt).
		WithResponse("response").
		WithModelID("model-A").
		WithEmbedding(emb).
		Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb))

	// Lookup with wrong model → miss
	result, err := c.Lookup(ctx, "ns", prompt, "model-B")
	require.NoError(t, err)
	assert.False(t, result.Hit)

	// Lookup with correct model → hit
	result, err = c.Lookup(ctx, "ns", prompt, "model-A")
	require.NoError(t, err)
	assert.True(t, result.Hit)
}

func TestSemantic_ExpiredFiltered(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(now)
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.5}, clock)
	ctx := context.Background()

	prompt := "hello world"
	emb, _ := embedder.Embed(ctx, prompt)
	entry := testutil.NewEntry().
		WithNamespace("ns").
		WithPrompt(prompt).
		WithResponse("response").
		WithModelID("model").
		WithEmbedding(emb).
		WithExpiresAt(now.Add(1 * time.Hour)).
		Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, idx.Add(ctx, entry.ID, emb))

	// Before expiry → hit
	result, err := c.Lookup(ctx, "ns", prompt, "model")
	require.NoError(t, err)
	assert.True(t, result.Hit)

	// After expiry → miss
	clock.Advance(2 * time.Hour)
	result, err = c.Lookup(ctx, "ns", prompt, "model")
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestSemantic_EmbeddingFailure_GracefulMiss(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	failingEmbedder := fake.NewFailing(dims, nil)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(failingEmbedder, idx, s, semantic.Config{Threshold: 0.5}, clock)
	ctx := context.Background()

	result, err := c.Lookup(ctx, "ns", "any prompt", "model")
	require.NoError(t, err)
	assert.False(t, result.Hit)
}

func TestSemantic_TopKRanking(t *testing.T) {
	s := memory.New()
	idx := flat.New(0)
	embedder := fake.New(dims)
	clock := testutil.NewFakeClock(time.Now())
	c := semantic.New(embedder, idx, s, semantic.Config{Threshold: 0.0, TopK: 3}, clock)
	ctx := context.Background()

	// Store multiple entries with known prompts
	prompts := []string{
		"password reset",
		"reset password steps",
		"account recovery",
		"billing information",
		"shipping address",
	}
	for _, p := range prompts {
		emb, _ := embedder.Embed(ctx, p)
		entry := testutil.NewEntry().
			WithNamespace("ns").
			WithPrompt(p).
			WithResponse("response for " + p).
			WithModelID("model").
			WithEmbedding(emb).
			Build()
		require.NoError(t, s.Put(ctx, entry))
		require.NoError(t, idx.Add(ctx, entry.ID, emb))
	}

	// Lookup should return a hit (the best match)
	result, err := c.Lookup(ctx, "ns", "password reset", "model")
	require.NoError(t, err)
	assert.True(t, result.Hit)
}
