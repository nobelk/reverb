// Package main demonstrates reverb's two-tier semantic caching.
//
// This example uses the fake embedder, which produces deterministic
// embeddings by hashing text. Because hashes of different strings are
// uncorrelated, two different prompts will always have LOW cosine similarity
// (well below any practical threshold). This means:
//
//   - Exact-match lookups (identical prompt text) → tier="exact", similarity=1.0
//   - Different prompts → always a cache miss with the fake embedder
//
// With a real embedder (OpenAI text-embedding-3-small, Ollama nomic-embed-text,
// etc.) semantically close prompts like "How do I reset my password?" and
// "password reset help" would score above the similarity threshold and return
// tier="semantic" hits. Swap fake.New(64) for an openai.New(...) or
// ollama.New(...) provider to observe true semantic matching in production.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"github.com/org/reverb/pkg/embedding/fake"
	"github.com/org/reverb/pkg/reverb"
	"github.com/org/reverb/pkg/store/memory"
	"github.com/org/reverb/pkg/vector/flat"
)

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Setup: build reverb client with in-process components
	// -------------------------------------------------------------------------
	// SimilarityThreshold is set lower than the default (0.95) to illustrate
	// the concept. With a real embedder, even 0.92 would catch close paraphrases.
	cfg := reverb.Config{
		DefaultNamespace:    "support-bot",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.92,
		SemanticTopK:        5,
		ScopeByModel:        true,
	}

	client, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New())
	if err != nil {
		log.Fatalf("failed to create reverb client: %v", err)
	}
	defer client.Close()

	fmt.Println("=== Reverb Two-Tier Semantic Cache Example ===")
	fmt.Println()

	// -------------------------------------------------------------------------
	// Section 1: Store entries
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 1: Storing cache entries ---")

	type entry struct {
		prompt   string
		response string
		sourceID string
		content  string // used to compute ContentHash
	}

	entries := []entry{
		{
			prompt:   "How do I reset my password?",
			response: "To reset your password: visit the login page, click 'Forgot Password', enter your registered email address, and follow the link sent to your inbox. The link expires after 30 minutes.",
			sourceID: "doc:password-reset",
			content:  "Password reset instructions v2",
		},
		{
			prompt:   "What are your pricing plans?",
			response: "We offer three plans: Starter ($9/mo, up to 3 users), Pro ($29/mo, up to 20 users), and Enterprise (custom pricing, unlimited users). Annual billing saves 20%.",
			sourceID: "doc:pricing",
			content:  "Pricing plans document v5",
		},
		{
			prompt:   "How do I delete my account?",
			response: "Account deletion is permanent and cannot be undone. Go to Account Settings > Danger Zone, click 'Delete Account', and confirm via the email sent to your address. All data is purged within 30 days.",
			sourceID: "doc:account-deletion",
			content:  "Account deletion policy v1",
		},
	}

	for _, e := range entries {
		hash := sha256.Sum256([]byte(e.content))
		stored, storeErr := client.Store(ctx, reverb.StoreRequest{
			Prompt:   e.prompt,
			ModelID:  "gpt-4o",
			Response: e.response,
			Sources: []reverb.SourceRef{
				{SourceID: e.sourceID, ContentHash: hash},
			},
			TTL: time.Hour,
		})
		if storeErr != nil {
			log.Fatalf("store failed for %q: %v", e.prompt, storeErr)
		}
		fmt.Printf("  stored  source=%-28s  entry_id=%s\n", e.sourceID, stored.ID)
	}
	fmt.Println()

	// -------------------------------------------------------------------------
	// Section 2: Exact match lookup
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 2: Exact match lookup ---")
	fmt.Println("  Prompt: \"How do I reset my password?\"")
	fmt.Println("  (identical text → tier=exact, similarity=1.0)")
	fmt.Println()

	exactResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "How do I reset my password?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(exactResp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Section 3: Cache miss
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 3: Cache miss (unrelated query) ---")
	fmt.Println("  Prompt: \"What is the weather today?\"")
	fmt.Println("  (no stored entry, no semantic match → miss)")
	fmt.Println()

	missResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "What is the weather today?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(missResp)
	fmt.Println()

	// NOTE: With the fake embedder, a paraphrase like "How can I reset my
	// password?" would also be a miss because hash-based embeddings of
	// different strings are uncorrelated. Replace fake.New(64) with a real
	// embedding provider to observe tier=semantic hits for close paraphrases.

	// -------------------------------------------------------------------------
	// Section 4: Namespace isolation
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 4: Namespace isolation ---")
	fmt.Println("  Storing an entry in namespace \"billing-bot\"...")

	billingHash := sha256.Sum256([]byte("Billing FAQ v3"))
	_, err = client.Store(ctx, reverb.StoreRequest{
		Namespace: "billing-bot",
		Prompt:    "How do I update my credit card?",
		ModelID:   "gpt-4o",
		Response:  "Go to Billing > Payment Methods and click 'Update Card'.",
		Sources: []reverb.SourceRef{
			{SourceID: "doc:billing-faq", ContentHash: billingHash},
		},
		TTL: time.Hour,
	})
	if err != nil {
		log.Fatalf("store failed: %v", err)
	}

	fmt.Println("  Looking up the same prompt from namespace \"support-bot\" → should miss")
	fmt.Println()

	nsIsolationResp, err := client.Lookup(ctx, reverb.LookupRequest{
		// No Namespace set → uses DefaultNamespace "support-bot"
		Prompt:  "How do I update my credit card?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(nsIsolationResp)
	fmt.Println()

	// Now look it up from the correct namespace → should hit
	fmt.Println("  Looking up from the correct namespace \"billing-bot\" → should hit")
	fmt.Println()

	nsHitResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Namespace: "billing-bot",
		Prompt:    "How do I update my credit card?",
		ModelID:   "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(nsHitResp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Section 5: Source lineage and invalidation
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 5: Source lineage and invalidation ---")

	// Store an entry tied to a specific source document
	pricingHash := sha256.Sum256([]byte("Pricing plans document v5"))
	_, err = client.Store(ctx, reverb.StoreRequest{
		Prompt:   "What are your pricing plans?",
		ModelID:  "gpt-4o",
		Response: "See pricing at example.com/pricing",
		Sources: []reverb.SourceRef{
			{SourceID: "doc:pricing", ContentHash: pricingHash},
		},
		TTL: time.Hour,
	})
	if err != nil {
		log.Fatalf("store failed: %v", err)
	}

	// Confirm it's cached
	fmt.Println("  Before invalidation:")
	beforeInvalid, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "What are your pricing plans?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(beforeInvalid)

	// Invalidate by source ID — simulates a document update triggering cache eviction
	count, err := client.Invalidate(ctx, "doc:pricing")
	if err != nil {
		log.Fatalf("invalidate failed: %v", err)
	}
	fmt.Printf("\n  Invalidated %d entry(s) linked to source \"doc:pricing\"\n\n", count)

	// Verify the entries are gone
	fmt.Println("  After invalidation:")
	afterInvalid, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "What are your pricing plans?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(afterInvalid)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Section 6: Stats
	// -------------------------------------------------------------------------
	fmt.Println("--- Section 6: Cache statistics ---")
	stats, err := client.Stats(ctx)
	if err != nil {
		log.Fatalf("stats failed: %v", err)
	}
	fmt.Printf("  total entries:        %d\n", stats.TotalEntries)
	fmt.Printf("  namespaces:           %v\n", stats.Namespaces)
	fmt.Printf("  exact hits total:     %d\n", stats.ExactHitsTotal)
	fmt.Printf("  semantic hits total:  %d\n", stats.SemanticHitsTotal)
	fmt.Printf("  misses total:         %d\n", stats.MissesTotal)
	fmt.Printf("  invalidations total:  %d\n", stats.InvalidationsTotal)
	fmt.Printf("  hit rate:             %.1f%%\n", stats.HitRate*100)
	fmt.Println()

	fmt.Println("=== Done ===")
}

func printResult(resp *reverb.LookupResponse) {
	if !resp.Hit {
		fmt.Println("  result: MISS")
		return
	}
	fmt.Printf("  result: HIT  tier=%-8s  similarity=%.4f\n", resp.Tier, resp.Similarity)
	fmt.Printf("          response: %s\n", resp.Entry.ResponseText)
}
