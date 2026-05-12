package reverb_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

// TestStreaming_StoreAndLookup verifies that an entry stored with chunks
// is queryable via Lookup with a reconstructed ResponseText, and that the
// chunks survive round-trip through the store.
func TestStreaming_StoreAndLookup(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()
	ctx := context.Background()

	chunks := []reverb.ResponseChunk{
		{Delta: "Hello"},
		{Delta: ", "},
		{Delta: "world", FinishReason: "stop"},
	}
	entry, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns",
		Prompt:    "greet",
		ModelID:   "gpt-4",
		Chunks:    chunks,
	})
	require.NoError(t, err)
	assert.Equal(t, "Hello, world", entry.ResponseText, "ResponseText must be reconstructed from chunks")
	require.Len(t, entry.Chunks, 3)

	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns", Prompt: "greet", ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)

	// Reconstruct via chunks and via ResponseText and ensure they agree.
	var b strings.Builder
	for _, ch := range resp.Entry.Chunks {
		b.WriteString(ch.Delta)
	}
	assert.Equal(t, resp.Entry.ResponseText, b.String())
}

// TestStreaming_PrefersChunksOverResponse: when both fields are set, chunks
// win and ResponseText is overridden.
func TestStreaming_PrefersChunksOverResponse(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	entry, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "ns", Prompt: "p", ModelID: "m",
		Response: "this should be ignored",
		Chunks:   []reverb.ResponseChunk{{Delta: "actual"}, {Delta: " text"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "actual text", entry.ResponseText)
}
