package reverb_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/internal/testutil"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/normalize/redactor/regex"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// TestRedactor_StoreAndLookup_BothRedacted verifies that with a default
// redactor configured, the stored PromptText is redacted and a redacted
// lookup form hits, while the raw form does *not* hit (because both flow
// through the same redactor before hashing).
func TestRedactor_StoreAndLookup_BothRedacted(t *testing.T) {
	c, _ := newRedactingClient(t)
	defer c.Close()
	ctx := context.Background()

	prompt := "my email is alice@example.com and my phone is 555-123-4567"
	entry, err := c.Store(ctx, reverb.StoreRequest{
		Namespace: "ns", Prompt: prompt, ModelID: "gpt-4", Response: "ok",
	})
	require.NoError(t, err)
	// Stored prompt must reflect the redacted form so debug output is PII-free.
	assert.Equal(t, "my email is [EMAIL] and my phone is [PHONE]", entry.PromptText)

	// Lookup with the same raw prompt → hits, because the lookup also runs
	// through the redactor before hashing.
	resp, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns", Prompt: prompt, ModelID: "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.Hit)

	// Lookup with a *different* PII value but the same redacted form must
	// also hit — that is the value-prop of the redactor.
	resp2, err := c.Lookup(ctx, reverb.LookupRequest{
		Namespace: "ns",
		Prompt:    "my email is bob@elsewhere.org and my phone is 555-987-6543",
		ModelID:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp2.Hit, "two prompts that differ only in redacted PII must share a cache entry")
}

// TestRedactor_DisabledByDefault verifies that without WithDefaultRedactor,
// no redaction occurs.
func TestRedactor_DisabledByDefault(t *testing.T) {
	c, _ := newTestClient(t, nil)
	defer c.Close()

	prompt := "my email is alice@example.com"
	entry, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "ns", Prompt: prompt, ModelID: "gpt-4", Response: "ok",
	})
	require.NoError(t, err)
	assert.Equal(t, prompt, entry.PromptText, "without redactor, prompt is stored verbatim")
}

// TestRedactor_PerNamespaceOverride verifies that an explicit nil override
// disables the default redactor for one namespace.
func TestRedactor_PerNamespaceOverride(t *testing.T) {
	s := memory.New()
	vi := flat.New(0)
	embedder := fake.New(64)
	clk := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		Clock:               clk,
	}
	c, err := reverb.New(cfg, embedder, s, vi,
		reverb.WithDefaultRedactor(regex.New()),
		reverb.WithNamespaceRedactor("raw", nil), // disable for "raw"
	)
	require.NoError(t, err)
	defer c.Close()

	// "raw" namespace: redactor disabled, prompt stored verbatim.
	entry, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "raw", Prompt: "alice@example.com",
		ModelID: "m", Response: "ok",
	})
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", entry.PromptText)

	// Default namespace: redactor active.
	entry2, err := c.Store(context.Background(), reverb.StoreRequest{
		Namespace: "ns", Prompt: "alice@example.com",
		ModelID: "m", Response: "ok",
	})
	require.NoError(t, err)
	assert.Equal(t, "[EMAIL]", entry2.PromptText)
}

// helper -------------------------------------------------------------------

func newRedactingClient(t *testing.T) (*reverb.Client, *memory.Store) {
	t.Helper()
	s := memory.New()
	vi := flat.New(0)
	embedder := fake.New(64)
	clk := testutil.NewFakeClock(time.Now())
	cfg := reverb.Config{
		DefaultNamespace:    "default",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		Clock:               clk,
	}
	c, err := reverb.New(cfg, embedder, s, vi,
		reverb.WithDefaultRedactor(regex.New()),
	)
	require.NoError(t, err)
	return c, s
}
