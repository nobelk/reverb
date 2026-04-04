# Task: Gap Analysis — Reverb Codebase vs Design Document

You are reviewing the Reverb project at `/Users/nobelk/sources/reverb`.

## Your Mission

Compare the **current codebase** against the **design document** at `reverb-design-doc.md` and produce a comprehensive gap analysis identifying all missing features, packages, files, and functionality.

## What Already Exists (Implemented)

The following components ARE implemented and working:

### Core packages (implemented):
- `internal/hashutil/` — SHA-256 helpers + tests
- `internal/retry/` — Retry with exponential backoff + tests
- `internal/testutil/` — FakeClock, EntryBuilder, embedding helpers
- `pkg/normalize/` — Prompt normalization + tests
- `pkg/store/store.go` — Store interface
- `pkg/store/conformance/` — Conformance test suite
- `pkg/store/memory/` — In-memory store + tests
- `pkg/vector/index.go` — VectorIndex interface
- `pkg/vector/conformance/` — Conformance test suite
- `pkg/vector/flat/` — Flat brute-force index + tests
- `pkg/vector/hnsw/` — HNSW index + tests
- `pkg/embedding/provider.go` — EmbeddingProvider interface
- `pkg/embedding/fake/` — Fake provider (includes FailingProvider)
- `pkg/embedding/openai/` — OpenAI provider + tests
- `pkg/embedding/ollama/` — Ollama provider (directory exists)
- `pkg/cache/tier.go` — CacheTier interface
- `pkg/cache/exact/` — Exact match cache + tests
- `pkg/cache/semantic/` — Semantic cache + tests
- `pkg/lineage/` — Lineage index + invalidator + tests
- `pkg/cdc/listener.go` — CDCListener interface
- `pkg/cdc/webhook/` — Webhook listener + tests
- `pkg/cdc/polling/` — Polling listener + tests
- `pkg/reverb/client.go` — Client facade with Lookup/Store/Invalidate/Stats/Close
- `pkg/reverb/config.go` — Config types + defaults + tests
- `pkg/server/http.go` — HTTP server with all REST endpoints + tests
- `pkg/metrics/metrics.go` — Metrics (exists)
- `pkg/metrics/tracing.go` — Tracing (exists)
- `cmd/reverb/main.go` — Standalone server binary
- `test/integration/` — client_test.go, http_test.go, helpers_test.go
- `test/docker-compose.yml` — Test infrastructure
- `.github/workflows/ci.yml` — CI pipeline
- `Makefile`, `Dockerfile`, `Dockerfile.test`

## What Is MISSING (Not Yet Implemented)

Based on my preliminary scan, these items from the design doc are NOT in the codebase:

### Missing Packages/Files:
1. **`pkg/store/badger/`** — BadgerDB persistent store (entire directory missing)
2. **`pkg/store/redis/`** — Redis store (entire directory missing)
3. **`pkg/cdc/nats/`** — NATS JetStream CDC listener (entire directory missing)
4. **`pkg/server/grpc.go`** — gRPC server implementation (missing)
5. **`pkg/server/grpc_test.go`** — gRPC server tests (missing)
6. **`pkg/server/proto/reverb.proto`** — Protobuf service definition (missing, directory exists but empty)
7. **`pkg/reverb/options.go`** — Functional options for client construction (missing)

### Missing Integration Tests:
8. **`test/integration/grpc_test.go`** — gRPC integration tests
9. **`test/integration/cdc_webhook_test.go`** — CDC webhook integration tests
10. **`test/integration/redis_test.go`** — Redis store integration tests
11. **`test/integration/nats_test.go`** — NATS CDC integration tests

### Missing Functionality in Existing Files:
12. **Background goroutines in Client** — The design doc specifies `invalidationLoop`, `expiryReaper`, `metricsUpdater`, and `cdcListener.Start` goroutines. None of these are in client.go.
13. **HitRate computation** — The design doc Stats endpoint includes `hit_rate` (rolling hit rate recomputed every 60s). The current Stats struct has no HitRate field.
14. **Config loading from YAML/env vars** — The design doc specifies YAML/env var hydration. cmd/reverb/main.go currently hardcodes in-memory defaults.
15. **Build tags** — Design doc specifies `//go:build nats` and `//go:build redis` tags for optional deps.

## Your Task

1. **Read the design document** (`reverb-design-doc.md`) thoroughly — all sections
2. **Read the existing source files** to verify what's actually implemented vs. stubbed
3. **Produce a comprehensive gap report** organized by priority

Write your output to: `/Users/nobelk/sources/reverb/.omc/research/gap-analysis.md`

### Output Format

Structure your report as:

```markdown
# Reverb Gap Analysis: Design Doc vs Implementation

## Summary
[Brief overview of completion status]

## Critical Gaps (Must-Have for MVP)
[Missing core functionality]

## Important Gaps (Should-Have)
[Missing features that are important but not blocking]

## Nice-to-Have Gaps
[Missing features that can be deferred]

## Per-Package Detail
[For each gap, specify: what's missing, what the design doc requires, effort estimate]
```

IMPORTANT: Be thorough. Check EVERY section of the design doc. Don't just confirm what I've listed — find things I may have missed. Read the actual source files to check for partial implementations or stubbed functions.
