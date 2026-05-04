# Changelog

All notable changes to Reverb will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and Reverb adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in [COMPATIBILITY.md](COMPATIBILITY.md).

Section conventions:

- **Added** — new features, endpoints, backends.
- **Changed** — non-breaking changes to existing behavior.
- **Breaking** — incompatible changes. Pre-1.0, these may appear in MINOR
  releases; post-1.0, only in MAJOR releases.
- **Deprecated** — still-working features slated for removal. Includes the
  target removal version and a migration path.
- **Removed** — features that were previously deprecated and are now gone.
- **Fixed** — bug fixes.
- **Security** — vulnerability fixes.

---

## [Unreleased]

### Added

- `reverb --validate` flag. Constructs the engine (store, embedder, vector
  index, client, auth) without binding listeners or calling the embedding
  provider, and exercises store connectivity via `Stats()`. Exits zero on
  success; non-zero with a structured slog error report on failure. The
  upgrade-testing checklist in [COMPATIBILITY.md](COMPATIBILITY.md#upgrade-testing-checklist)
  references this flow.
- `examples/openai-chat/`. Self-contained, runnable demonstration of
  Reverb in front of either OpenAI or Ollama: cold miss, exact hit, and
  semantic hit on a paraphrased prompt, with similarity and source
  lineage printed.
- `reverb.WithRebuildVectorIndex(bool)` option and
  `store.rebuild_vector_index_on_startup` YAML knob. When enabled, `reverb.New`
  scans the configured store at boot and re-adds every non-expired entry's
  embedding to the in-memory vector index before returning, closing the
  semantic-tier cold-start window on durable backends.
- RUNBOOK.md §Persistence & Restart Behavior documenting Badger durability
  (`SyncWrites: false` default), Redis persistence expectations (AOF vs RDB),
  and the vector-index rebuild trade-off.
- `TestBadgerSurvivesClose` — asserts all entries and secondary indices
  (hash, lineage, stats) survive a close/reopen cycle on the same directory.
- Restart test suite at `pkg/reverb/restart_test.go` covering the cold-index
  default path, eager-rebuild reconciliation, skip behavior for expired and
  embedding-missing entries, and the full Badger restart cycle.
- `internal/clock` — single shared `Clock` interface and `Real()` constructor,
  replacing four byte-identical declarations across `pkg/reverb`,
  `pkg/cache/exact`, `pkg/cache/semantic`, and `pkg/limiter`.
- `pkg/vector.CosineSimilarity` — single canonical implementation, replacing
  duplicate copies in `pkg/vector/flat` and `pkg/vector/hnsw`.
- `metrics.StoreTracer` and `metrics.NewStoreTracer(backend)` for store
  implementations to attach `gen_ai.cache.store.backend` and per-op span
  attributes uniformly. Span names and attribute shapes are unchanged.

### Changed

- HNSW search hot path no longer recomputes cosine similarity during the
  final sort: `searchLayer` now returns `[]scoredNode` so the score computed
  inside the heap is reused. Eliminates O(n log n) cosine calls per
  `Index.Search`.
- Webhook listener (`pkg/cdc/webhook`) now selects on `r.Context().Done()`
  when sending to the events channel, returning HTTP 503 if the request is
  cancelled (e.g. during shutdown) instead of blocking the handler.
- Modernized to current Go idioms: `interface{}` → `any`, `sort.Slice` →
  `slices.SortFunc(cmp.Compare(...))`, manual min/max → `min`/`max` builtins,
  `math/rand` → `math/rand/v2` (HNSW now uses `rand.NewPCG`).
- `pkg/reverb/client.go` invalidation loop refactored: extracted
  `processChange`, simplified the shutdown-drain branch, and ensured pending
  events are flushed if the CDC channel closes during drain.
- Store implementations (memory, redis, badger) now route OTel span
  bookkeeping through `metrics.StoreTracer` and `metrics.RecordError`
  instead of inline `otel.Tracer(...).Start(...)` and
  `span.RecordError(err); span.SetStatus(...)` boilerplate.

### Removed

- `pkg/cache/exact.Clock`, `pkg/cache/semantic.Clock`, `pkg/limiter.Clock`,
  and their `realClock` types. Use `internal/clock.Clock` (or, externally,
  any type satisfying `interface{ Now() time.Time }` — Go's structural
  interface satisfaction means existing test fakes continue to work).
  `reverb.Clock` is preserved as a type alias.
- `internal/hashutil.SHA256` and `internal/hashutil.ContentHash` — no
  production callers; tests inline `crypto/sha256` directly.
- `pkg/lineage.Invalidator.Run` — duplicated the batch-flush pattern
  already implemented in `Client.invalidationLoop`. Production code uses
  the latter; the only test referencing `Run` was removed.
- `pkg/embedding/fake.errFakeEmbeddingFailure` custom error type, replaced
  with `errors.New(...)`. `ErrFakeEmbeddingFailure` is unchanged.
- `metrics.StartStoreGetSpan`, `StartStorePutSpan`, `StartStoreDeleteSpan`,
  `StartStoreDeleteBatchSpan` on `*Tracer`. These had no production callers
  and have been superseded by the `*StoreTracer` methods which carry the
  backend label.

### Fixed

- `pkg/reverb/client.go` invalidation loop: pending events buffered in
  memory at shutdown are now flushed before return when the CDC channel
  closes during the drain phase.

---

## [0.1.0] — 2026-04-21

Initial tagged release. Establishes the public Go API, wire protocols, and
backend surface that Reverb is willing to support under the
[compatibility policy](COMPATIBILITY.md).

### Added

- MCP (Model Context Protocol) JSON-RPC wrapper at `pkg/server/mcp` — marked **experimental** in [COMPATIBILITY.md](COMPATIBILITY.md#transport-stability); the tool surface may change without a deprecation window.
- Auth and multi-tenant scoping (`pkg/auth`) with bearer token on HTTP and
  API-key metadata on gRPC. Marked **beta**.
- OpenTelemetry tracing and Prometheus metrics (`pkg/metrics`).
- Benchmark suite under `benchmark/`.
- Explicit embedding dimensionality validation at `Client` construction.
- Production runbook ([`RUNBOOK.md`](RUNBOOK.md)).
- Compatibility and release policy ([`COMPATIBILITY.md`](COMPATIBILITY.md))
  and this changelog.

### Public Go API

- `pkg/reverb` — `Client`, `Config`, `LookupRequest`, `StoreRequest`, `Response`.
- `pkg/store` — `Store` interface with `memory`, `redis`, and `badger`
  implementations. Shared conformance suite at `pkg/store/conformance`.
- `pkg/vector` — `Index` interface with `flat` (brute-force) and `hnsw`
  implementations. Shared conformance suite at `pkg/vector/conformance`.
- `pkg/embedding` — `Provider` interface with `openai`, `ollama`, and `fake`
  implementations.
- `pkg/cdc` — `Listener` interface with `webhook`, `polling`, and `nats`
  implementations.

### Transports

- Embedded library (`pkg/reverb`).
- HTTP REST at `/v1/*` (`pkg/server` HTTP server).
- gRPC `reverb.v1.ReverbService` (`pkg/server` gRPC server, proto at
  `pkg/server/proto/reverb.proto`).
- MCP JSON-RPC (experimental, `pkg/server/mcp`).

### Standalone binary

- `cmd/reverb` with YAML config via `--config`, flag overrides, and
  `REVERB_*` environment variable overrides for secrets.
- Multi-stage production Dockerfile and test-runner Dockerfile.

### Toolchain

- Go 1.25+.

### Known gaps

(None.)

---

[Unreleased]: https://github.com/nobelk/reverb/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/nobelk/reverb/releases/tag/v0.1.0
