package lineage_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/lineage"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func TestInvalidator_InvalidateOnHashChange(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)
	ctx := context.Background()

	oldHash := sha256.Sum256([]byte("old content"))
	newHash := sha256.Sum256([]byte("new content"))

	entry := testutil.NewEntry().WithSource("src-1", "old content").
		WithEmbedding([]float32{1, 0, 0}).Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, vi.Add(ctx, entry.ID, entry.Embedding))

	// Verify entry exists
	got, _ := s.Get(ctx, entry.ID)
	require.NotNil(t, got)

	// Hash changed → invalidate
	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "src-1",
		ContentHash: newHash,
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	_ = oldHash
	assert.Equal(t, 1, count)

	// Entry should be deleted
	got, _ = s.Get(ctx, entry.ID)
	assert.Nil(t, got)
}

func TestInvalidator_NoInvalidateOnSameHash(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)
	ctx := context.Background()

	content := "same content"
	sameHash := sha256.Sum256([]byte(content))

	entry := testutil.NewEntry().WithSource("src-1", content).Build()
	require.NoError(t, s.Put(ctx, entry))

	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "src-1",
		ContentHash: sameHash,
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Entry should still exist
	got, _ := s.Get(ctx, entry.ID)
	assert.NotNil(t, got)
}

func TestInvalidator_InvalidateOnDeletion(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)
	ctx := context.Background()

	entry := testutil.NewEntry().WithSource("src-1", "content").
		WithEmbedding([]float32{1, 0, 0}).Build()
	require.NoError(t, s.Put(ctx, entry))
	require.NoError(t, vi.Add(ctx, entry.ID, entry.Embedding))

	// Zero hash → deletion
	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "src-1",
		ContentHash: [32]byte{},
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	got, _ := s.Get(ctx, entry.ID)
	assert.Nil(t, got)
}

func TestInvalidator_BatchAccumulation(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)
	ctx := context.Background()

	// Create 5 entries all referencing the same source
	for i := 0; i < 5; i++ {
		entry := testutil.NewEntry().WithSource("src-1", "content").Build()
		require.NoError(t, s.Put(ctx, entry))
	}

	// Invalidate all
	count, err := inv.ProcessEvent(ctx, lineage.ChangeEvent{
		SourceID:    "src-1",
		ContentHash: [32]byte{}, // deletion
		Timestamp:   time.Now(),
	})
	require.NoError(t, err)
	assert.Equal(t, 5, count)
}

func TestInvalidator_ConcurrentInvalidation(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	idx := lineage.NewIndex(s)
	inv := lineage.NewInvalidator(s, vi, idx, nil)
	ctx := context.Background()

	// Create entries for different sources
	for i := 0; i < 10; i++ {
		entry := testutil.NewEntry().WithSource("src-A", "contentA").Build()
		require.NoError(t, s.Put(ctx, entry))
	}
	for i := 0; i < 10; i++ {
		entry := testutil.NewEntry().WithSource("src-B", "contentB").Build()
		require.NoError(t, s.Put(ctx, entry))
	}

	// Process events concurrently
	done := make(chan struct{}, 2)
	go func() {
		inv.ProcessEvent(ctx, lineage.ChangeEvent{
			SourceID:    "src-A",
			ContentHash: [32]byte{},
			Timestamp:   time.Now(),
		})
		done <- struct{}{}
	}()
	go func() {
		inv.ProcessEvent(ctx, lineage.ChangeEvent{
			SourceID:    "src-B",
			ContentHash: [32]byte{},
			Timestamp:   time.Now(),
		})
		done <- struct{}{}
	}()
	<-done
	<-done

	stats, _ := s.Stats(ctx)
	assert.Equal(t, int64(0), stats.TotalEntries)
}

