// Package main runs Reverb in front of an LLM call to demonstrate the
// semantic-cache value proposition end-to-end.
//
// The program performs three lookup rounds against the same cache:
//
//  1. Cold cache, exact prompt          → tier="" (miss). The response is
//     "generated" by the example, then stored.
//  2. Same exact prompt, second time    → tier="exact"   (similarity = 1.0).
//  3. Paraphrased prompt                → tier="semantic" (similarity ≥
//     SimilarityThreshold). Same answer; no LLM round-trip.
//
// Round 3 is the point of the demo: a paraphrase that the exact-match cache
// cannot catch is still served from cache because the embedding of the
// paraphrase is close in vector space to the embedding of the original.
//
// Two providers are supported:
//
//   - openai: requires OPENAI_API_KEY; uses text-embedding-3-small (1536-d).
//   - ollama: requires a reachable Ollama daemon; uses nomic-embed-text by
//     default. Set OLLAMA_HOST to point at a non-localhost daemon.
//
// Pick one with --provider=openai or --provider=ollama. The example does
// NOT auto-detect — picking the wrong provider should fail fast rather than
// silently fall back to the offline path on a workstation that has both.
//
// Exit codes: 0 on success (round 3 produced a semantic hit at or above the
// threshold), 1 on any error or if round 3 missed. CI relies on the non-
// zero exit to gate the merge.
package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/embedding/ollama"
	"github.com/nobelk/reverb/pkg/embedding/openai"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

const (
	originalPrompt   = "How do I reset my password on the support portal?"
	paraphrasedPrompt = "What's the procedure to change my support portal password?"
	cannedResponse   = "Visit the login page, click 'Forgot Password', enter your registered email, " +
		"and follow the link sent to your inbox. The reset link expires after 30 minutes."
	docSourceID = "doc:password-reset"
	docContent  = "Password reset instructions v2 — 2026-04 revision"
	modelID     = "gpt-4o"
)

func main() {
	provider := flag.String("provider", "", "embedding provider: openai | ollama (required)")
	threshold := flag.Float64("threshold", 0.85, "similarity threshold; round 3 must score at or above this to count as a semantic hit")
	flag.Parse()

	if err := run(*provider, float32(*threshold)); err != nil {
		log.Fatalf("openai-chat example failed: %v", err)
	}
}

func run(provider string, threshold float32) error {
	embedder, providerLabel, err := buildEmbedder(provider)
	if err != nil {
		return err
	}

	cfg := reverb.Config{
		DefaultNamespace:    "demo",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: threshold,
		SemanticTopK:        5,
		ScopeByModel:        true,
	}

	client, err := reverb.New(cfg, embedder, memory.New(), flat.New(0))
	if err != nil {
		return fmt.Errorf("reverb.New: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("=== Reverb demo (provider=%s, threshold=%.2f) ===\n\n", providerLabel, threshold)

	// --- Round 1: cold miss -------------------------------------------------
	fmt.Println("--- Round 1: cold cache, exact prompt → miss ---")
	fmt.Printf("  prompt: %q\n", originalPrompt)
	r1, err := client.Lookup(ctx, reverb.LookupRequest{Prompt: originalPrompt, ModelID: modelID})
	if err != nil {
		return fmt.Errorf("round 1 lookup: %w", err)
	}
	if r1.Hit {
		return fmt.Errorf("round 1: expected miss, got hit (tier=%s)", r1.Tier)
	}
	fmt.Println("  result: tier=miss")

	// Simulate calling the LLM and storing the response.
	hash := sha256.Sum256([]byte(docContent))
	if _, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   originalPrompt,
		ModelID:  modelID,
		Response: cannedResponse,
		Sources:  []reverb.SourceRef{{SourceID: docSourceID, ContentHash: hash}},
		TTL:      time.Hour,
	}); err != nil {
		return fmt.Errorf("round 1 store: %w", err)
	}
	fmt.Println("  → stored; next round should be an exact hit")
	fmt.Println()

	// --- Round 2: exact hit -------------------------------------------------
	fmt.Println("--- Round 2: identical prompt → exact hit ---")
	fmt.Printf("  prompt: %q\n", originalPrompt)
	r2, err := client.Lookup(ctx, reverb.LookupRequest{Prompt: originalPrompt, ModelID: modelID})
	if err != nil {
		return fmt.Errorf("round 2 lookup: %w", err)
	}
	if !r2.Hit || r2.Tier != "exact" {
		return fmt.Errorf("round 2: expected tier=exact hit, got hit=%v tier=%q", r2.Hit, r2.Tier)
	}
	fmt.Printf("  result: tier=%s similarity=%.4f\n", r2.Tier, r2.Similarity)
	fmt.Println()

	// --- Round 3: semantic hit on a paraphrase ------------------------------
	fmt.Println("--- Round 3: paraphrased prompt → semantic hit ---")
	fmt.Printf("  prompt: %q\n", paraphrasedPrompt)
	r3, err := client.Lookup(ctx, reverb.LookupRequest{Prompt: paraphrasedPrompt, ModelID: modelID})
	if err != nil {
		return fmt.Errorf("round 3 lookup: %w", err)
	}
	if !r3.Hit || r3.Tier != "semantic" {
		return fmt.Errorf("round 3: expected tier=semantic hit at threshold %.2f; got hit=%v tier=%q similarity=%.4f. "+
			"This usually means the embedder is producing low-correlation vectors for the paraphrase pair (e.g., a fake embedder is in use), "+
			"or the threshold is set too high for the model in use.",
			threshold, r3.Hit, r3.Tier, r3.Similarity)
	}
	sources := make([]string, 0, len(r3.Entry.SourceHashes))
	for _, src := range r3.Entry.SourceHashes {
		sources = append(sources, src.SourceID)
	}
	fmt.Printf("  result: tier=%s similarity=%.4f sources=%v\n", r3.Tier, r3.Similarity, sources)
	fmt.Printf("  response: %s\n", r3.Entry.ResponseText)
	fmt.Println()

	fmt.Println("=== Done — round 3's semantic hit is the demo's payoff ===")
	return nil
}

// buildEmbedder picks the embedding provider based on the --provider flag.
// Returning (embedding.Provider, label, error) gives the caller a stable
// label to print and a single error path for missing-credential cases.
func buildEmbedder(provider string) (embedding.Provider, string, error) {
	switch provider {
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, "", fmt.Errorf("--provider=openai requires OPENAI_API_KEY")
		}
		model := envDefault("OPENAI_EMBED_MODEL", "text-embedding-3-small")
		return openai.New(openai.Config{
			APIKey:     key,
			Model:      model,
			Dimensions: 1536,
		}), fmt.Sprintf("openai (%s)", model), nil

	case "ollama":
		host := envDefault("OLLAMA_HOST", "http://localhost:11434")
		model := envDefault("OLLAMA_EMBED_MODEL", "nomic-embed-text")
		return ollama.New(host, model), fmt.Sprintf("ollama (%s @ %s)", model, host), nil

	case "":
		return nil, "", fmt.Errorf("--provider is required (openai | ollama)")
	default:
		return nil, "", fmt.Errorf("unknown --provider %q (want openai | ollama)", provider)
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
