package hnsw_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nobelk/reverb/pkg/vector"
	"github.com/nobelk/reverb/pkg/vector/conformance"
	"github.com/nobelk/reverb/pkg/vector/hnsw"
)

func TestHNSWIndexConformance(t *testing.T) {
	conformance.RunVectorIndexConformance(t, func(t *testing.T, dims int) vector.Index {
		return hnsw.New(hnsw.Config{M: 16, EfConstruction: 200, EfSearch: 100}, dims)
	})
}

// TestHNSW_DimensionValidation verifies that adding a vector with the wrong number of
// dimensions returns an error and does not corrupt the index.
func TestHNSW_DimensionValidation(t *testing.T) {
	// Index configured for 4 dimensions.
	idx := hnsw.New(hnsw.Config{M: 16, EfConstruction: 200, EfSearch: 100}, 4)
	ctx := context.Background()

	// Correct dimensions should succeed.
	err := idx.Add(ctx, "ok", []float32{1, 0, 0, 0})
	if err != nil {
		t.Fatalf("Add with correct dims: %v", err)
	}

	// Wrong dimensions (too few) should fail.
	err = idx.Add(ctx, "bad-short", []float32{1, 0})
	if err == nil {
		t.Fatal("expected error for 2-dim vector in 4-dim index")
	}

	// Wrong dimensions (too many) should fail.
	err = idx.Add(ctx, "bad-long", []float32{1, 0, 0, 0, 0, 0, 0, 0})
	if err == nil {
		t.Fatal("expected error for 8-dim vector in 4-dim index")
	}

	// Index should only contain the valid vector.
	if idx.Len() != 1 {
		t.Errorf("expected 1 vector in index, got %d", idx.Len())
	}

	// Search should still work correctly.
	results, err := idx.Search(ctx, []float32{1, 0, 0, 0}, 1, 0.0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].ID != "ok" {
		t.Errorf("unexpected search results: %v", results)
	}
}

// TestHNSW_DimensionInferredFromFirstVector verifies that when dims=0, the first
// vector's length is used for validation of subsequent vectors.
func TestHNSW_DimensionInferredFromFirstVector(t *testing.T) {
	idx := hnsw.New(hnsw.Config{}, 0)
	ctx := context.Background()

	// First add should succeed and lock in dimensions.
	if err := idx.Add(ctx, "first", []float32{1, 0, 0}); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	// Second add with same dims should succeed.
	if err := idx.Add(ctx, "second", []float32{0, 1, 0}); err != nil {
		t.Fatalf("second Add: %v", err)
	}

	// Third add with different dims should fail.
	err := idx.Add(ctx, "third", []float32{1, 0, 0, 0, 0})
	if err == nil {
		t.Fatal("expected error for mismatched dimensions after inference")
	}

	if idx.Len() != 2 {
		t.Errorf("expected 2 vectors, got %d", idx.Len())
	}
}

// TestHNSW_PruneConnectionsBidirectional verifies that after pruning, all edges remain
// bidirectional — no dangling one-directional edges are left in the graph.
func TestHNSW_PruneConnectionsBidirectional(t *testing.T) {
	// M=2 means nodes get pruned aggressively, exercising pruneConnections heavily.
	idx := hnsw.New(hnsw.Config{M: 2, EfConstruction: 10, EfSearch: 10}, 4)
	ctx := context.Background()

	// Insert enough nodes to trigger many pruning events.
	vecs := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
		{1, 1, 0, 0},
		{1, 0, 1, 0},
		{0, 1, 1, 0},
		{1, 1, 1, 0},
		{1, 1, 1, 1},
		{0, 0, 1, 1},
	}
	for i, v := range vecs {
		if err := idx.Add(ctx, fmt.Sprintf("n%d", i), v); err != nil {
			t.Fatalf("Add n%d: %v", i, err)
		}
	}

	if err := idx.CheckBidirectional(); err != nil {
		t.Errorf("bidirectionality violated after insertions: %v", err)
	}

	// Verify search still works and returns results.
	results, err := idx.Search(ctx, []float32{1, 0, 0, 0}, 3, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results, got none")
	}
}

// TestHNSW_DeleteAfterPrune verifies that deleting nodes after pruning leaves the graph
// in a consistent bidirectional state and search returns correct results.
func TestHNSW_DeleteAfterPrune(t *testing.T) {
	idx := hnsw.New(hnsw.Config{M: 2, EfConstruction: 10, EfSearch: 10}, 4)
	ctx := context.Background()

	vecs := map[string][]float32{
		"a": {1, 0, 0, 0},
		"b": {0, 1, 0, 0},
		"c": {0, 0, 1, 0},
		"d": {0, 0, 0, 1},
		"e": {1, 1, 0, 0},
		"f": {1, 0, 1, 0},
		"g": {0, 1, 1, 0},
		"h": {1, 1, 1, 0},
	}
	// Insert in a stable order.
	insertOrder := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, id := range insertOrder {
		if err := idx.Add(ctx, id, vecs[id]); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}

	// Delete some nodes.
	for _, id := range []string{"b", "d", "f"} {
		if err := idx.Delete(ctx, id); err != nil {
			t.Fatalf("Delete %s: %v", id, err)
		}
	}

	if err := idx.CheckBidirectional(); err != nil {
		t.Errorf("bidirectionality violated after deletions: %v", err)
	}

	// Search must not panic and must return results.
	results, err := idx.Search(ctx, []float32{1, 0, 0, 0}, 3, 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected search results after deletions, got none")
	}

	// Deleted nodes must not appear in results.
	deleted := map[string]bool{"b": true, "d": true, "f": true}
	for _, r := range results {
		if deleted[r.ID] {
			t.Errorf("deleted node %q appeared in search results", r.ID)
		}
	}
}

// TestHNSW_ManyInsertDeleteCycles stress-tests the graph with repeated insert/delete
// cycles to verify that graph integrity is maintained throughout.
func TestHNSW_ManyInsertDeleteCycles(t *testing.T) {
	idx := hnsw.New(hnsw.Config{M: 3, EfConstruction: 15, EfSearch: 15}, 4)
	ctx := context.Background()

	base := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
		{0, 0, 0, 1},
		{1, 1, 0, 0},
	}

	// Seed initial nodes.
	for i, v := range base {
		if err := idx.Add(ctx, fmt.Sprintf("seed%d", i), v); err != nil {
			t.Fatalf("seed Add: %v", err)
		}
	}

	// Run insert/delete cycles.
	for cycle := 0; cycle < 20; cycle++ {
		id := fmt.Sprintf("cycle%d", cycle)
		v := []float32{
			float32(cycle%3) * 0.5,
			float32((cycle+1)%3) * 0.5,
			float32((cycle+2)%3) * 0.5,
			float32(cycle%2) * 0.5,
		}

		if err := idx.Add(ctx, id, v); err != nil {
			t.Fatalf("cycle %d Add: %v", cycle, err)
		}

		if err := idx.CheckBidirectional(); err != nil {
			t.Errorf("cycle %d: bidirectionality violated after Add: %v", cycle, err)
		}

		// Search must not panic.
		if _, err := idx.Search(ctx, v, 3, 0); err != nil {
			t.Fatalf("cycle %d Search after Add: %v", cycle, err)
		}

		// Delete every other cycle's node to stress removals.
		if cycle%2 == 1 {
			prev := fmt.Sprintf("cycle%d", cycle-1)
			if err := idx.Delete(ctx, prev); err != nil {
				t.Fatalf("cycle %d Delete: %v", cycle, err)
			}

			if err := idx.CheckBidirectional(); err != nil {
				t.Errorf("cycle %d: bidirectionality violated after Delete: %v", cycle, err)
			}

			if _, err := idx.Search(ctx, v, 3, 0); err != nil {
				t.Fatalf("cycle %d Search after Delete: %v", cycle, err)
			}
		}
	}
}
