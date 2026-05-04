package main

import (
	"context"
	"testing"
	"time"

	"github.com/nobelk/reverb/pkg/reverb"
)

// TestValidateEngine_DefaultConfig is the primary check behind `reverb
// --validate`: from the default (memory store, fake embedder, flat vector
// index) the engine must wire up cleanly. Operators rely on this exit-0
// signal before promoting a config to production, so a regression here
// silently breaks an upgrade-testing checklist step in COMPATIBILITY.md.
func TestValidateEngine_DefaultConfig(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := validateEngine(ctx, cfg); err != nil {
		t.Fatalf("validateEngine: %v", err)
	}
}

// TestValidateEngine_BadgerEphemeral uses a t.TempDir() as the badger
// directory to confirm the durable-store path also wires through validate
// without leaving residue. Catches the "badger creates lock file but engine
// can't open it" class of bug.
func TestValidateEngine_BadgerEphemeral(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.Store.Backend = "badger"
	cfg.Store.BadgerPath = t.TempDir()
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := validateEngine(ctx, cfg); err != nil {
		t.Fatalf("validateEngine: %v", err)
	}
}

// TestValidateEngine_UnreachableRedis confirms validate fails fast (rather
// than hanging or spuriously passing) when the configured store is
// unreachable. Uses an unroutable port on localhost to trigger a connect
// error within the validate timeout.
func TestValidateEngine_UnreachableRedis(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.Store.Backend = "redis"
	cfg.Store.RedisAddr = "127.0.0.1:1" // reserved/unused port
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := validateEngine(ctx, cfg); err == nil {
		t.Fatal("expected validateEngine to fail against unreachable redis, got nil")
	}
}
