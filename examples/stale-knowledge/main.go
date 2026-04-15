// Package main demonstrates reverb preventing stale cached answers after a
// knowledge base update — its primary value proposition over a plain TTL cache.
//
// Scenario: A customer support chatbot caches LLM responses linked to a pricing
// document. When the pricing document changes, a webhook fires and reverb
// automatically invalidates all cached responses derived from the old document.
// Without reverb, users would see outdated pricing until the TTL expires.
//
// This example uses Redis for persistent storage and the CDC webhook listener
// for production-realistic invalidation. It is intentionally exact-match only
// (using the fake embedder), because the focus is on source-aware invalidation,
// not semantic reuse quality.
//
// Note: The flat.New(0) vector index is in-process memory and does not survive
// restarts. Exact-match lookups (backed by Redis) do persist across restarts,
// but semantic search state would be lost. This is acceptable here because the
// example relies solely on exact-match lookups.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/nobelk/reverb/pkg/cdc/webhook"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	redistore "github.com/nobelk/reverb/pkg/store/redis"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func main() {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Setup: connect to Redis and build reverb client with CDC webhook listener
	// -------------------------------------------------------------------------
	redisAddr := envOr("REDIS_ADDR", "localhost:6379")

	store, err := redistore.New(redisAddr, "", 0, "reverb:")
	if err != nil {
		log.Fatalf("failed to connect to Redis at %s: %v", redisAddr, err)
	}

	webhookAddr := envOr("WEBHOOK_ADDR", ":9091")
	listener := webhook.New(webhook.Config{
		Addr: webhookAddr,
		Path: "/hooks/source-changed",
	})

	cfg := reverb.Config{
		DefaultNamespace:    "support-bot",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
	}

	client, err := reverb.New(cfg, fake.New(64), store, flat.New(0),
		reverb.WithCDCListener(listener),
	)
	if err != nil {
		log.Fatalf("failed to create reverb client: %v", err)
	}
	defer client.Close()

	// Give the webhook HTTP server a moment to start.
	time.Sleep(100 * time.Millisecond)

	fmt.Println("=== Reverb Stale Knowledge Prevention Example ===")
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 1: Cold cache lookup — expect MISS
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 1: Cold cache lookup ---")
	prompt := "What are your pricing plans?"
	fmt.Printf("  prompt:  %q\n", prompt)

	resp, err := client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  prompt,
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(resp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 2: Store the LLM response, linked to source doc:pricing
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 2: Store response from LLM (linked to doc:pricing) ---")
	pricingV1 := "Starter $9/mo, Pro $29/mo, Enterprise custom."
	hashV1 := sha256.Sum256([]byte("pricing-document-v1"))

	stored, err := client.Store(ctx, reverb.StoreRequest{
		Prompt:   prompt,
		ModelID:  "gpt-4o",
		Response: pricingV1,
		Sources: []reverb.SourceRef{
			{SourceID: "doc:pricing", ContentHash: hashV1},
		},
	})
	if err != nil {
		log.Fatalf("store failed: %v", err)
	}
	fmt.Printf("  stored entry_id=%s  source=doc:pricing\n", stored.ID)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 3: Same question again → expect HIT (saved an LLM call)
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 3: Same question again → cache HIT ---")
	fmt.Printf("  prompt:  %q\n", prompt)

	resp, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  prompt,
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(resp)
	fmt.Println()
	fmt.Println("  [saved ~$0.03 — avoided redundant LLM call]")
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 4: Pricing document updated — fire webhook with NEW content hash
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 4: Pricing document updated! Firing webhook... ---")
	hashV2 := sha256.Sum256([]byte("pricing-document-v2"))

	webhookURL := fmt.Sprintf("http://localhost%s/hooks/source-changed", webhookAddr)
	body, _ := json.Marshal(map[string]string{
		"source_id":    "doc:pricing",
		"content_hash": hex.EncodeToString(hashV2[:]),
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	})
	httpResp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("webhook POST failed: %v", err)
	}
	httpResp.Body.Close()
	fmt.Printf("  POST %s → %d\n", webhookURL, httpResp.StatusCode)

	// Poll until invalidation takes effect (CDC is async with 500ms batch flush).
	fmt.Println("  waiting for invalidation...")
	invalidated := pollForMiss(ctx, client, prompt, "gpt-4o", 5*time.Second)
	if !invalidated {
		log.Fatal("  ERROR: entry was not invalidated within timeout")
	}
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 5: Same question after source change → expect MISS
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 5: Same question after source change → MISS ---")
	fmt.Printf("  prompt:  %q\n", prompt)

	resp, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  prompt,
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(resp)
	fmt.Println()
	fmt.Println("  [stale answer evicted — user will NOT see old pricing]")
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 6: Re-store with updated pricing
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 6: Re-store with updated pricing ---")
	pricingV2 := "Starter $19/mo, Growth $49/mo, Enterprise custom."

	stored, err = client.Store(ctx, reverb.StoreRequest{
		Prompt:   prompt,
		ModelID:  "gpt-4o",
		Response: pricingV2,
		Sources: []reverb.SourceRef{
			{SourceID: "doc:pricing", ContentHash: hashV2},
		},
	})
	if err != nil {
		log.Fatalf("store failed: %v", err)
	}
	fmt.Printf("  stored entry_id=%s  source=doc:pricing\n", stored.ID)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 7: Final lookup → expect HIT with correct new pricing
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 7: Final lookup → HIT with correct new pricing ---")
	fmt.Printf("  prompt:  %q\n", prompt)

	resp, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  prompt,
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(resp)
	fmt.Println()

	// -------------------------------------------------------------------------
	// Step 8: Verify same-hash webhook does NOT invalidate (idempotency)
	// -------------------------------------------------------------------------
	fmt.Println("--- Step 8: Same content hash webhook → no invalidation ---")
	body, _ = json.Marshal(map[string]string{
		"source_id":    "doc:pricing",
		"content_hash": hex.EncodeToString(hashV2[:]),
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
	})
	httpResp, err = http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("webhook POST failed: %v", err)
	}
	httpResp.Body.Close()
	fmt.Printf("  POST same content_hash → %d\n", httpResp.StatusCode)

	// Wait for CDC flush to ensure the event is processed.
	time.Sleep(800 * time.Millisecond)

	resp, err = client.Lookup(ctx, reverb.LookupRequest{
		Prompt:  prompt,
		ModelID: "gpt-4o",
	})
	if err != nil {
		log.Fatalf("lookup failed: %v", err)
	}
	printResult(resp)
	if resp.Hit {
		fmt.Println("  [correct: same content hash does NOT trigger invalidation]")
	} else {
		fmt.Println("  [unexpected: entry was invalidated by same content hash]")
	}
	fmt.Println()

	// -------------------------------------------------------------------------
	// Stats
	// -------------------------------------------------------------------------
	fmt.Println("--- Stats ---")
	stats, err := client.Stats(ctx)
	if err != nil {
		log.Fatalf("stats failed: %v", err)
	}
	fmt.Printf("  exact hits:       %d\n", stats.ExactHitsTotal)
	fmt.Printf("  misses:           %d\n", stats.MissesTotal)
	fmt.Printf("  invalidations:    %d\n", stats.InvalidationsTotal)
	fmt.Printf("  hit rate:         %.1f%%\n", stats.HitRate*100)
	fmt.Println()

	fmt.Println("=== Without reverb, Steps 5-7 would have returned the OLD pricing. ===")
	fmt.Println("=== Reverb's source lineage tracking prevented serving stale data.  ===")
}

// pollForMiss polls Lookup until it returns a miss or the deadline expires.
// This accounts for CDC's asynchronous batching (500ms flush interval).
func pollForMiss(ctx context.Context, client *reverb.Client, prompt, modelID string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			resp, err := client.Lookup(ctx, reverb.LookupRequest{
				Prompt:  prompt,
				ModelID: modelID,
			})
			if err != nil {
				continue
			}
			if !resp.Hit {
				return true
			}
		}
	}
}

func printResult(resp *reverb.LookupResponse) {
	if !resp.Hit {
		fmt.Println("  result:  MISS (no cached entry)")
		return
	}
	fmt.Printf("  result:  HIT  tier=%-8s  similarity=%.4f\n", resp.Tier, resp.Similarity)
	fmt.Printf("  response: %q\n", resp.Entry.ResponseText)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
