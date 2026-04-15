package flat

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/nobelk/reverb/pkg/vector"
)

// Index is a brute-force linear scan vector index.
// O(n) search. Suitable for up to ~50K entries. Thread-safe via sync.RWMutex.
type Index struct {
	mu      sync.RWMutex
	dims    int
	vectors map[string][]float32
}

// New creates a new flat vector index with the given vector dimensionality.
// If dims is 0, the dimensionality is inferred from the first vector added.
func New(dims int) *Index {
	return &Index{
		dims:    dims,
		vectors: make(map[string][]float32),
	}
}

func (idx *Index) Add(_ context.Context, id string, vec []float32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	// Validate vector dimensionality.
	if idx.dims > 0 && len(vec) != idx.dims {
		return fmt.Errorf("vector dimension mismatch: index configured for %d dimensions, got %d", idx.dims, len(vec))
	}
	if idx.dims == 0 {
		idx.dims = len(vec)
	}

	// Store a copy to prevent external mutation
	v := make([]float32, len(vec))
	copy(v, vec)
	idx.vectors[id] = v
	return nil
}

func (idx *Index) Search(_ context.Context, query []float32, k int, minScore float32) ([]vector.SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.vectors) == 0 {
		return nil, nil
	}

	type scored struct {
		id    string
		score float32
	}

	var candidates []scored
	for id, vec := range idx.vectors {
		score := cosineSimilarity(query, vec)
		if score >= minScore {
			candidates = append(candidates, scored{id: id, score: score})
		}
	}

	// Sort descending by score
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})

	if len(candidates) > k {
		candidates = candidates[:k]
	}

	results := make([]vector.SearchResult, len(candidates))
	for i, c := range candidates {
		results[i] = vector.SearchResult{ID: c.id, Score: c.score}
	}
	return results, nil
}

func (idx *Index) Delete(_ context.Context, id string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.vectors, id)
	return nil
}

func (idx *Index) Len() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.vectors)
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
