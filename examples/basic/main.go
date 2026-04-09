package main

import (
	"context"
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
	// 1. Build client from in-process components (no external dependencies)
	// -------------------------------------------------------------------------
	embedder := fake.New(64)
	store := memory.New()
	index := flat.New()

	cfg := reverb.Config{
		DefaultNamespace:    "support",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
	}

	client, err := reverb.New(cfg, embedder, store, index)
	if err != nil {
		log.Fatalf("failed to create reverb client: %v", err)
	}
	defer client.Close()

	fmt.Println("=== Reverb Basic Example ===")
	fmt.Println()

	// -------------------------------------------------------------------------
	// 2. Store several cache entries covering different support topics
	// -------------------------------------------------------------------------
	entries := []struct {
		prompt   string
		response string
		sourceID string
	}{
		{
			prompt:   "How do I reset my password?",
			response: "To reset your password, visit the login page and click 'Forgot password'. Enter your email address and follow the instructions sent to your inbox.",
			sourceID: "doc:password-reset",
		},
		{
			prompt:   "How do I update my billing information?",
			response: "You can update billing information under Account Settings > Billing. Changes take effect on the next billing cycle.",
			sourceID: "doc:billing",
		},
		{
			prompt:   "How do I delete my account?",
			response: "Account deletion is permanent. Go to Account Settings > Danger Zone and click 'Delete Account'. You will receive a confirmation email.",
			sourceID: "doc:account-deletion",
		},
		{
			prompt:   "What is the refund policy?",
			response: "We offer full refunds within 30 days of purchase. Contact support@example.com with your order number to request a refund.",
			sourceID: "doc:refund-policy",
		},
	}

	fmt.Println("--- Storing cache entries ---")
	for _, e := range entries {
		entry, err := client.Store(ctx, reverb.StoreRequest{
			Prompt:    e.prompt,
			ModelID:   "gpt-4o",
			Response:  e.response,
			Sources:   []reverb.SourceRef{{SourceID: e.sourceID}},
			TTL:       time.Hour,
		})
		if err != nil {
			log.Fatalf("store failed: %v", err)
		}
		fmt.Printf("  stored [%s] -> entry ID: %s\n", e.sourceID, entry.ID)
	}
	fmt.Println()

	// -------------------------------------------------------------------------
	// 3. Exact-match hit — same prompt text (normalized identically)
	// -------------------------------------------------------------------------
	fmt.Println("--- Exact-match lookup ---")
	exactResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "How do I reset my password?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printLookupResult("How do I reset my password?", exactResp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// 4. Another exact-match hit
	// -------------------------------------------------------------------------
	fmt.Println("--- Exact-match lookup (billing) ---")
	billingResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "How do I update my billing information?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printLookupResult("How do I update my billing information?", billingResp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// 5. Cache miss — unrelated prompt with no stored entry
	// -------------------------------------------------------------------------
	fmt.Println("--- Cache miss (unknown topic) ---")
	missResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "What is the capital of France?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printLookupResult("What is the capital of France?", missResp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// 6. Cache stats
	// -------------------------------------------------------------------------
	fmt.Println("--- Cache stats ---")
	stats, err := client.Stats(ctx)
	if err != nil {
		log.Fatalf("stats failed: %v", err)
	}
	fmt.Printf("  total entries:  %d\n", stats.TotalEntries)
	fmt.Printf("  exact hits:     %d\n", stats.ExactHitsTotal)
	fmt.Printf("  semantic hits:  %d\n", stats.SemanticHitsTotal)
	fmt.Printf("  misses:         %d\n", stats.MissesTotal)
	fmt.Printf("  hit rate:       %.1f%%\n", stats.HitRate*100)
	fmt.Println()

	// -------------------------------------------------------------------------
	// 7. Invalidate entries by source ID, then confirm the entry is gone
	// -------------------------------------------------------------------------
	fmt.Println("--- Invalidation by source ID ---")
	count, err := client.Invalidate(ctx, "doc:password-reset")
	if err != nil {
		log.Fatalf("invalidate failed: %v", err)
	}
	fmt.Printf("  invalidated %d entry(s) for source 'doc:password-reset'\n", count)

	// Lookup the same prompt again — should now be a miss
	postInvalidResp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  "How do I reset my password?",
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	fmt.Print("  after invalidation: ")
	printLookupResult("How do I reset my password?", postInvalidResp)
	fmt.Println()

	fmt.Println("=== Done ===")
}

func printLookupResult(prompt string, resp *reverb.LookupResponse) {
	if !resp.Hit {
		fmt.Printf("  MISS  prompt=%q\n", prompt)
		return
	}
	fmt.Printf("  HIT   tier=%-8s similarity=%.4f  prompt=%q\n",
		resp.Tier, resp.Similarity, prompt)
	fmt.Printf("        response: %s\n", resp.Entry.ResponseText)
}
