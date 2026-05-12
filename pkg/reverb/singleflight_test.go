package reverb_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

// TestLookupOrCall_StressCoalesces verifies that when N callers race for the
// same miss, fill is invoked exactly once and every caller receives the same
// stored response.
func TestLookupOrCall_StressCoalesces(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	const concurrency = 100
	var fillCount atomic.Int32
	fill := func(_ context.Context) (reverb.StoreRequest, error) {
		fillCount.Add(1)
		// Sleep long enough for all goroutines to pile up on the in-flight
		// fill; without this the test can race-pass even without singleflight.
		time.Sleep(50 * time.Millisecond)
		return reverb.StoreRequest{
			Response: "the answer",
		}, nil
	}

	req := reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "What is the meaning of life?",
		ModelID:   "gpt-4",
	}

	var (
		wg      sync.WaitGroup
		results = make([]*reverb.LookupResponse, concurrency)
		errs    = make([]error, concurrency)
		hits    = make([]bool, concurrency)
	)
	wg.Add(concurrency)
	for i := range concurrency {
		go func(idx int) {
			defer wg.Done()
			resp, hit, err := c.LookupOrCall(context.Background(), req, fill)
			results[idx] = resp
			errs[idx] = err
			hits[idx] = hit
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), fillCount.Load(), "fill must be called exactly once across %d racers", concurrency)
	for i := range concurrency {
		require.NoError(t, errs[i])
		require.NotNil(t, results[i], "result %d nil", i)
		require.NotNil(t, results[i].Entry, "entry %d nil", i)
		assert.Equal(t, "the answer", results[i].Entry.ResponseText)
		assert.False(t, hits[i], "this race always fills, never a pre-existing hit")
	}

	// Subsequent calls should now hit the cache (no fill).
	resp, hit, err := c.LookupOrCall(context.Background(), req, fill)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, int32(1), fillCount.Load(), "no additional fill after cache populated")
	assert.Equal(t, "the answer", resp.Entry.ResponseText)
}

// TestLookupOrCall_FillError propagates the error to all coalesced callers
// and stores nothing.
func TestLookupOrCall_FillError(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	wantErr := errors.New("upstream unreachable")
	fill := func(_ context.Context) (reverb.StoreRequest, error) {
		return reverb.StoreRequest{}, wantErr
	}

	req := reverb.LookupRequest{Namespace: "ns", Prompt: "hi", ModelID: "gpt-4"}

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			_, _, err := c.LookupOrCall(context.Background(), req, fill)
			assert.ErrorIs(t, err, wantErr)
		}()
	}
	wg.Wait()

	// Verify nothing was cached.
	lookup, err := c.Lookup(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, lookup.Hit)
}

// TestLookupOrCall_NilFill falls back to a plain Lookup when no fill is given.
func TestLookupOrCall_NilFill(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	resp, hit, err := c.LookupOrCall(context.Background(), reverb.LookupRequest{
		Namespace: "ns", Prompt: "missing", ModelID: "gpt-4",
	}, nil)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.NotNil(t, resp)
	assert.False(t, resp.Hit)
}

// TestLookupOrCall_ImmediateHitSkipsFill verifies the fast path: if the cache
// already has an entry, fill is never called.
func TestLookupOrCall_ImmediateHitSkipsFill(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	_, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "ns", Prompt: "cached?", ModelID: "gpt-4", Response: "yes",
	})
	require.NoError(t, err)

	var fillCount atomic.Int32
	fill := func(_ context.Context) (reverb.StoreRequest, error) {
		fillCount.Add(1)
		return reverb.StoreRequest{Response: "should not be used"}, nil
	}

	resp, hit, err := c.LookupOrCall(context.Background(), reverb.LookupRequest{
		Namespace: "ns", Prompt: "cached?", ModelID: "gpt-4",
	}, fill)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, int32(0), fillCount.Load())
	assert.Equal(t, "yes", resp.Entry.ResponseText)
}
