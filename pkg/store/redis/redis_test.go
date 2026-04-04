package redis

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/org/reverb/internal/testutil"
	"github.com/org/reverb/pkg/store"
	"github.com/org/reverb/pkg/store/conformance"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := New("localhost:6379", "", 15, "reverb-test:")
	if err != nil {
		t.Skipf("cannot create redis store: %v", err)
	}
	// Check connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.client.Ping(ctx).Err(); err != nil {
		s.Close()
		t.Skipf("Redis not available: %v", err)
	}
	// Flush test DB before each test
	if err := s.client.FlushDB(ctx).Err(); err != nil {
		s.Close()
		t.Skipf("Redis FlushDB failed: %v", err)
	}
	t.Cleanup(func() {
		ctx2 := context.Background()
		s.client.FlushDB(ctx2)
		s.Close()
	})
	return s
}

func TestRedisConformance(t *testing.T) {
	conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
		return newTestStore(t)
	})
}

// TestConcurrentIncrementHit spawns 10 goroutines each calling IncrementHit
// 10 times on the same entry. The final HitCount must be exactly 100, proving
// no increments are lost due to concurrent read-modify-write races.
func TestConcurrentIncrementHit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entry := testutil.NewEntry().Build()
	require.NoError(t, s.Put(ctx, entry))

	const goroutines = 10
	const hitsEach = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < hitsEach; j++ {
				require.NoError(t, s.IncrementHit(ctx, entry.ID))
			}
		}()
	}
	wg.Wait()

	got, err := s.Get(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(goroutines*hitsEach), got.HitCount)
	assert.False(t, got.LastHitAt.IsZero())
}

// TestPutOverwriteCleansOldIndices verifies that when an entry is overwritten
// with a different namespace/hash and different source references, the old hash
// index and old lineage memberships are removed.
func TestPutOverwriteCleansOldIndices(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	oldHash := [32]byte{1}
	newHash := [32]byte{2}

	// Write v1 with namespace "ns1", explicit hash, source "src-old"
	e1 := testutil.NewEntry().WithNamespace("ns1").WithPromptHash(oldHash).WithSource("src-old", "content1").Build()
	require.NoError(t, s.Put(ctx, e1))

	// Confirm old hash index and lineage exist
	byHash, err := s.GetByHash(ctx, "ns1", oldHash)
	require.NoError(t, err)
	require.NotNil(t, byHash, "hash index should exist after first Put")

	oldLineage, err := s.ListBySource(ctx, "src-old")
	require.NoError(t, err)
	assert.Contains(t, oldLineage, e1.ID, "lineage should contain entry before overwrite")

	// Overwrite same ID with namespace "ns2", different hash, source "src-new"
	e2 := testutil.NewEntry().WithNamespace("ns2").WithPromptHash(newHash).WithSource("src-new", "content2").Build()
	e2.ID = e1.ID
	require.NoError(t, s.Put(ctx, e2))

	// Old hash index must be gone
	gone, err := s.GetByHash(ctx, "ns1", oldHash)
	require.NoError(t, err)
	assert.Nil(t, gone, "old hash index should be removed after overwrite")

	// Old lineage must be cleaned
	oldLineageAfter, err := s.ListBySource(ctx, "src-old")
	require.NoError(t, err)
	assert.NotContains(t, oldLineageAfter, e1.ID, "old lineage should be removed after overwrite")

	// New hash index and lineage must exist
	newByHash, err := s.GetByHash(ctx, "ns2", newHash)
	require.NoError(t, err)
	require.NotNil(t, newByHash, "new hash index should exist after overwrite")

	newLineage, err := s.ListBySource(ctx, "src-new")
	require.NoError(t, err)
	assert.Contains(t, newLineage, e2.ID, "new lineage should exist after overwrite")
}

// Ensure store.Store interface is satisfied (compile-time check).
var _ store.Store = (*Store)(nil)
