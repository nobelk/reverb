package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// TestHTTPClient_AgainstRealServer wires a real reverb engine + HTTP server
// behind httptest and drives it through the CLI's HTTP wire client. This
// is the end-to-end check that the JSON shapes the CLI emits stay aligned
// with `pkg/server/http.go` decoders — if either side drifts, this test
// breaks before users do.
func TestHTTPClient_AgainstRealServer(t *testing.T) {
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	rev, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New(0))
	if err != nil {
		t.Fatalf("reverb.New: %v", err)
	}
	defer rev.Close()

	httpSrv := server.NewHTTPServer(rev, ":0", nil)
	ts := httptest.NewServer(httpSrv)
	defer ts.Close()

	cli, err := newHTTPClient(ts.URL, "", 5*time.Second)
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()

	// Empty cache: stats should report zeros without erroring.
	stats, err := cli.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalEntries != 0 {
		t.Errorf("fresh cache total_entries=%d want 0", stats.TotalEntries)
	}

	// Round-trip: store, then lookup the same prompt — should be a hit.
	storeResp, err := cli.Store(ctx, &storeReq{
		Namespace: "ns1",
		Prompt:    "what is the capital of France",
		Response:  "Paris",
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if storeResp.ID == "" {
		t.Fatalf("Store returned empty ID")
	}

	lookupResp, err := cli.Lookup(ctx, &lookupReq{
		Namespace: "ns1",
		Prompt:    "what is the capital of France",
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !lookupResp.Hit {
		t.Errorf("expected exact hit, got miss; tier=%q", lookupResp.Tier)
	}
	if lookupResp.Tier != "exact" {
		t.Errorf("tier=%q want exact", lookupResp.Tier)
	}

	// Invalidate by source — this entry has no sources, so count is 0;
	// the call still succeeds and that's what we're proving here.
	inv, err := cli.Invalidate(ctx, "no-such-source")
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if inv.InvalidatedCount != 0 {
		t.Errorf("invalidated_count=%d want 0", inv.InvalidatedCount)
	}
}
