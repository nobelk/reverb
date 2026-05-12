// Package main demonstrates the PII-redaction hook.
//
// Builds a Reverb client wired with the regex redactor, stores a prompt
// containing an email + phone, then asserts:
//
//  1. The stored entry's PromptText shows the placeholder forms — no PII at
//     rest in the cache.
//  2. A lookup of the *original* prompt is a hit (lookup also flows through
//     the redactor).
//  3. A lookup with the *same redacted shape but different PII values* is
//     also a hit — that's the value-prop of the redactor.
//  4. A lookup of the *non-redacted* form is a miss when redaction is
//     disabled (control case).
//
// Exit codes: 0 on success, 1 on assertion failure. CI relies on the non-
// zero exit to gate the merge.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/normalize/redactor/regex"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

const (
	originalPrompt = "my email is alice@example.com and my phone is 555-123-4567"
	otherPIIForm   = "my email is bob@elsewhere.org and my phone is 555-987-6543"
	expectedStored = "my email is [EMAIL] and my phone is [PHONE]"
	cannedResponse = "Acknowledged. Will follow up via the channels you listed."
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("pii-redaction example failed: %v", err)
	}
}

func run() error {
	cfg := reverb.Config{
		DefaultNamespace:    "demo",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.99,
		SemanticTopK:        5,
	}
	client, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New(0),
		reverb.WithDefaultRedactor(regex.New()),
	)
	if err != nil {
		return fmt.Errorf("reverb.New: %w", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("=== PII redaction demo ===")
	fmt.Printf("  raw prompt:       %q\n", originalPrompt)

	entry, err := client.Store(ctx, reverb.StoreRequest{
		Prompt: originalPrompt, ModelID: "demo-model", Response: cannedResponse,
	})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	fmt.Printf("  stored prompt:    %q   ← no PII at rest\n", entry.PromptText)
	if entry.PromptText != expectedStored {
		return fmt.Errorf("expected stored prompt %q, got %q", expectedStored, entry.PromptText)
	}

	r1, err := client.Lookup(ctx, reverb.LookupRequest{Prompt: originalPrompt, ModelID: "demo-model"})
	if err != nil {
		return fmt.Errorf("lookup #1: %w", err)
	}
	if !r1.Hit {
		return fmt.Errorf("expected hit on original prompt, got miss")
	}
	fmt.Printf("  lookup(original): tier=%s\n", r1.Tier)

	r2, err := client.Lookup(ctx, reverb.LookupRequest{Prompt: otherPIIForm, ModelID: "demo-model"})
	if err != nil {
		return fmt.Errorf("lookup #2: %w", err)
	}
	if !r2.Hit {
		return fmt.Errorf("expected hit on alt-PII prompt (same redacted form), got miss")
	}
	fmt.Printf("  lookup(other PII, same redacted shape): tier=%s   ← cache hit despite different PII values\n", r2.Tier)

	// Control: a fresh client *without* a redactor must miss on the same
	// alt-PII prompt — proving redaction is what made the previous hit
	// possible, not some accidental match.
	control, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New(0))
	if err != nil {
		return fmt.Errorf("control reverb.New: %w", err)
	}
	defer control.Close()
	if _, err := control.Store(ctx, reverb.StoreRequest{
		Prompt: originalPrompt, ModelID: "demo-model", Response: cannedResponse,
	}); err != nil {
		return fmt.Errorf("control store: %w", err)
	}
	r3, err := control.Lookup(ctx, reverb.LookupRequest{Prompt: otherPIIForm, ModelID: "demo-model"})
	if err != nil {
		return fmt.Errorf("control lookup: %w", err)
	}
	if r3.Hit {
		return fmt.Errorf("control expected miss without redactor, got hit")
	}
	fmt.Println("  control: redactor disabled → alt-PII lookup misses (as expected)")

	fmt.Println("=== OK ===")
	return nil
}
