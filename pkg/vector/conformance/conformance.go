package conformance

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/vector"
)

// RunVectorIndexConformance runs the full conformance suite against any Index implementation.
func RunVectorIndexConformance(t *testing.T, factory func(t *testing.T, dims int) vector.Index) {
	dims := 8

	t.Run("AddAndSearchExactMatch", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		vec := randomVector(dims)
		require.NoError(t, idx.Add(ctx, "v1", vec))
		results, err := idx.Search(ctx, vec, 1, 0.99)
		require.NoError(t, err)
		require.Len(t, results, 1)
		assert.Equal(t, "v1", results[0].ID)
		assert.InDelta(t, 1.0, results[0].Score, 0.01)
	})

	t.Run("SearchRespectsMinScore", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		require.NoError(t, idx.Add(ctx, "v1", randomVector(dims)))
		results, _ := idx.Search(ctx, orthogonalVector(dims), 5, 0.99)
		assert.Empty(t, results)
	})

	t.Run("SearchTopK", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		for i := 0; i < 20; i++ {
			require.NoError(t, idx.Add(ctx, fmt.Sprintf("v%d", i), randomVector(dims)))
		}
		results, err := idx.Search(ctx, randomVector(dims), 3, 0.0)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(results), 3)
	})

	t.Run("SearchResultsOrderedByScore", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		for i := 0; i < 50; i++ {
			require.NoError(t, idx.Add(ctx, fmt.Sprintf("v%d", i), randomVector(dims)))
		}
		results, _ := idx.Search(ctx, randomVector(dims), 10, 0.0)
		for i := 1; i < len(results); i++ {
			assert.GreaterOrEqual(t, results[i-1].Score, results[i].Score,
				"results should be sorted descending by score")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		vec := randomVector(dims)
		require.NoError(t, idx.Add(ctx, "v1", vec))
		require.NoError(t, idx.Delete(ctx, "v1"))
		results, _ := idx.Search(ctx, vec, 1, 0.99)
		assert.Empty(t, results)
	})

	t.Run("DeleteNonexistent_NoError", func(t *testing.T) {
		idx := factory(t, dims)
		err := idx.Delete(context.Background(), "nonexistent")
		assert.NoError(t, err)
	})

	t.Run("Len", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		assert.Equal(t, 0, idx.Len())
		require.NoError(t, idx.Add(ctx, "v1", randomVector(dims)))
		require.NoError(t, idx.Add(ctx, "v2", randomVector(dims)))
		assert.Equal(t, 2, idx.Len())
		require.NoError(t, idx.Delete(ctx, "v1"))
		assert.Equal(t, 1, idx.Len())
	})

	t.Run("AddOverwrite", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		vec1 := randomVector(dims)
		vec2 := randomVector(dims)
		require.NoError(t, idx.Add(ctx, "v1", vec1))
		require.NoError(t, idx.Add(ctx, "v1", vec2))
		assert.Equal(t, 1, idx.Len(), "overwrite should not create duplicate")
		results, _ := idx.Search(ctx, vec2, 1, 0.99)
		require.Len(t, results, 1)
		assert.Equal(t, "v1", results[0].ID)
	})

	t.Run("EmptyIndex", func(t *testing.T) {
		idx := factory(t, dims)
		results, err := idx.Search(context.Background(), randomVector(dims), 5, 0.0)
		require.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("AddRejectsDimensionMismatch", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		// First add establishes/validates dimensions.
		vec := randomVector(dims)
		require.NoError(t, idx.Add(ctx, "v1", vec))
		// Adding a vector with wrong dimensions must fail.
		wrongVec := randomVector(dims * 2)
		err := idx.Add(ctx, "v2", wrongVec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dimension mismatch")
		// Original vector should still be searchable (use same vector as query for exact match).
		results, err := idx.Search(ctx, vec, 1, 0.99)
		require.NoError(t, err)
		assert.Len(t, results, 1)
	})

	t.Run("AddRejectsDimensionMismatch_Configured", func(t *testing.T) {
		// Create an index with explicit dimensions and immediately try wrong dims.
		idx := factory(t, 16)
		ctx := context.Background()
		wrongVec := randomVector(8)
		err := idx.Add(ctx, "v1", wrongVec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dimension mismatch")
		assert.Equal(t, 0, idx.Len(), "rejected vector should not be stored")
	})

	t.Run("AddAcceptsMatchingDimensions", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		// Multiple adds with correct dimensions should all succeed.
		for i := 0; i < 5; i++ {
			require.NoError(t, idx.Add(ctx, fmt.Sprintf("v%d", i), randomVector(dims)))
		}
		assert.Equal(t, 5, idx.Len())
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		idx := factory(t, dims)
		ctx := context.Background()
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				id := fmt.Sprintf("v%d", i)
				_ = idx.Add(ctx, id, randomVector(dims))
				_, _ = idx.Search(ctx, randomVector(dims), 3, 0.0)
				if i%3 == 0 {
					_ = idx.Delete(ctx, id)
				}
			}(i)
		}
		wg.Wait()
	})
}

func randomVector(dims int) []float32 {
	vec := make([]float32, dims)
	for i := range vec {
		vec[i] = rand.Float32()*2 - 1
	}
	return l2Normalize(vec)
}

func orthogonalVector(dims int) []float32 {
	// Create a vector that is close to orthogonal to most random vectors
	// by putting all weight on a single dimension
	vec := make([]float32, dims)
	vec[0] = 1.0
	for i := 1; i < dims; i++ {
		vec[i] = 0
	}
	return vec
}

func l2Normalize(vec []float32) []float32 {
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}
