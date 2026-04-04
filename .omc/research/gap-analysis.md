# Reverb Gap Analysis: Design Doc vs Implementation

## Summary

The codebase implements roughly **60-65%** of the design doc. The core lookup/store flow works end-to-end with in-memory backends and HTTP API. However, all durable storage backends, the gRPC API, NATS CDC, Ollama embeddings, background goroutines, Prometheus/OpenTelemetry observability, and config loading are missing or stubbed.

**Implemented:** Core interfaces, memory store, flat + HNSW vector indexes, exact + semantic cache tiers, lineage/invalidation engine, webhook + polling CDC, OpenAI embeddings, fake embeddings, HTTP server (all endpoints), normalize, hashutil, retry, testutil, basic metrics counters, unit tests for all implemented packages, some integration tests.

**Missing:** 14 distinct feature gaps across storage, networking, observability, configuration, and background processing.

---

## Critical Gaps (Must-Have for MVP)

### 1. BadgerDB Persistent Store — `pkg/store/badger/`
- **Status:** Entire directory missing
- **Design doc:** Section 6.3 — Durable embedded key-value store. Entries as protobuf-serialized bytes. Secondary indices for hash lookup and source-lineage lookup via key prefixes.
- **Impact:** Without this, Reverb loses all cached data on restart. The only store backend is `memory.Store`.
- **Effort:** Medium — implement Store interface against BadgerDB v4 API, add conformance tests.
- **Files needed:** `badger.go`, `badger_test.go`

### 2. Background Goroutines in Client — `pkg/reverb/client.go`
- **Status:** Not implemented. No `invalidationLoop`, `expiryReaper`, `metricsUpdater`, or `cdcListener.Start`.
- **Design doc:** Section 15.2 — Four background goroutines started in `New()`, all stopped on `Close()`.
- **Impact:** Expired entries are never cleaned up. CDC events are never processed automatically. Metrics gauge never updates. The invalidation subsystem only works via manual `Invalidate()` calls.
- **Effort:** Medium — wire CDC listener, add expiry scanner loop, add metrics updater loop, manage context/cancel/waitgroup lifecycle.

### 3. gRPC Server — `pkg/server/grpc.go` + `pkg/server/proto/reverb.proto`
- **Status:** Both files missing. `proto/` directory exists but is empty.
- **Design doc:** Section 13 — Full protobuf service definition with Lookup, Store, Invalidate, DeleteEntry, GetStats RPCs. Tests use bufconn.
- **Impact:** Design doc specifies both HTTP and gRPC as first-class server modes. gRPC is required for standalone service mode.
- **Effort:** Medium-High — write .proto file, generate Go code, implement gRPC server handlers, write bufconn-based tests.
- **Files needed:** `proto/reverb.proto`, `grpc.go`, `grpc_test.go`

### 4. Redis Store — `pkg/store/redis/`
- **Status:** Entire directory missing
- **Design doc:** Section 6.3 — Redis 7+ backend. Entries in Redis Hashes. Hash-lookup via sorted set. Source-lineage via Redis Sets. Native TTL support.
- **Impact:** No distributed/shared cache backend. Required for multi-instance deployments.
- **Effort:** Medium — implement Store interface using go-redis/v9, add conformance tests + integration tests.
- **Files needed:** `redis.go`, `redis_test.go`

---

## Important Gaps (Should-Have)

### 5. NATS CDC Listener — `pkg/cdc/nats/`
- **Status:** Entire directory missing
- **Design doc:** Section 6.4 — Subscribes to NATS JetStream subject, expects JSON-encoded ChangeEvent.
- **Impact:** Only webhook and polling CDC modes available. NATS is the recommended production CDC mode.
- **Effort:** Low-Medium — implement Listener interface against nats.go client.
- **Files needed:** `nats.go`, `nats_test.go`
- **Note:** Design doc specifies `//go:build nats` build tag.

### 6. Ollama Embedding Provider — `pkg/embedding/ollama/`
- **Status:** Directory exists but is **completely empty** (no .go files)
- **Design doc:** Section 6.1 — Calls local Ollama instance, supports any embedding model (e.g., `nomic-embed-text`), no batching (serialized calls).
- **Impact:** No local/offline embedding option. Only OpenAI (cloud) and fake (test) providers available.
- **Effort:** Low — implement Provider interface with HTTP calls to Ollama API.
- **Files needed:** `ollama.go`, `ollama_test.go`

### 7. Prometheus Metrics Integration — `pkg/metrics/metrics.go`
- **Status:** Only basic atomic counters exist (49 lines). No Prometheus client integration whatsoever.
- **Design doc:** Section 14.1 — 10 Prometheus metrics with `reverb_` prefix: counters, histograms, gauges with namespace/tier/provider labels.
- **Impact:** No production observability. Can't monitor cache hit rates, latencies, or error rates in Prometheus/Grafana.
- **Effort:** Medium — add prometheus/client_golang integration, register all metrics, instrument lookup/store/embed/search paths.
- **Missing metrics:** `reverb_lookups_total`, `reverb_lookup_duration_seconds`, `reverb_stores_total`, `reverb_store_duration_seconds`, `reverb_invalidations_total`, `reverb_entries_total`, `reverb_embedding_duration_seconds`, `reverb_embedding_errors_total`, `reverb_vector_search_duration_seconds`, `reverb_hit_rate`

### 8. OpenTelemetry Tracing — `pkg/metrics/tracing.go`
- **Status:** Stub only — just a `TracingConfig` struct (8 lines). No actual OpenTelemetry integration.
- **Design doc:** Section 14.2 — Spans for `reverb.lookup`, `reverb.store`, `reverb.invalidate` with attributes. Child spans for `embedding.embed`, `vector.search`, `store.get`, `store.put`.
- **Impact:** No distributed tracing support.
- **Effort:** Medium — add OTel SDK, create span wrappers, instrument all key paths.

### 9. Config Loading (YAML/Env Vars) — `cmd/reverb/main.go`
- **Status:** Server binary hardcodes `memory.Store`, `flat.Index`, `fake.Provider`. No config file loading.
- **Design doc:** Section 10 — Config hydrated from YAML file, env vars, or programmatic construction. Example `reverb.yaml` with all options. `--config` CLI flag.
- **Impact:** The standalone server is unusable in production — can't configure any backend, embedding provider, or CDC mode.
- **Effort:** Medium — add YAML parsing (gopkg.in/yaml.v3), env var overlay, factory functions to construct stores/providers/indexes from config.

### 10. Functional Options — `pkg/reverb/options.go`
- **Status:** File missing
- **Design doc:** Section 5 package structure — `options.go` listed as "Functional options for client construction"
- **Impact:** No ergonomic library API for constructing clients with optional overrides.
- **Effort:** Low — add `Option` type and `With*` functions.

---

## Nice-to-Have Gaps

### 11. Stats `hit_rate` Field — `pkg/server/http.go`
- **Status:** HTTP Stats response struct (`statsResp`) omits `hit_rate`. The `MetricsSnapshot.HitRate()` method exists in metrics.go but is never called.
- **Design doc:** Section 12.5 — Stats endpoint returns `hit_rate: 0.742`.
- **Effort:** Trivial — add field to statsResp, compute from existing counters.

### 12. Missing Integration Tests
- **Status:** Missing 4 integration test files:
  - `test/integration/grpc_test.go` — gRPC endpoint tests
  - `test/integration/cdc_webhook_test.go` — CDC webhook end-to-end
  - `test/integration/redis_test.go` — Redis store integration
  - `test/integration/nats_test.go` — NATS CDC integration
- **Design doc:** Section 17.4 — Full list of integration test scenarios.
- **Effort:** Medium (depends on implementing the underlying features first).

### 13. Build Tags for Optional Dependencies
- **Status:** No build tags used anywhere.
- **Design doc:** Section 19.1 — `//go:build nats` for NATS CDC, `//go:build redis` for Redis store.
- **Impact:** Once NATS/Redis are implemented, build tags keep the core library dependency-free.
- **Effort:** Low — add tags to relevant files.

### 14. Client Doesn't Use Metrics Collector
- **Status:** `Client` tracks hits/misses/invalidations via its own `sync.Mutex`-protected `int64` fields instead of using the `metrics.Collector` with atomic counters.
- **Design doc:** Client should use the `metrics.Collector` and the Prometheus-integrated version.
- **Effort:** Low — replace manual counters with `metrics.Collector` usage.

---

## Per-Package Detail

| Package | Design Doc Section | Status | Missing |
|---|---|---|---|
| `internal/hashutil` | 5, 7 | Complete | - |
| `internal/retry` | 5, 16.3 | Complete | - |
| `internal/testutil` | 5, 17.1 | Complete | - |
| `pkg/normalize` | 5, 9 | Complete | - |
| `pkg/store/store.go` | 6.3 | Complete | - |
| `pkg/store/conformance` | 17.2.1 | Complete | - |
| `pkg/store/memory` | 6.3 | Complete | - |
| `pkg/store/badger` | 6.3 | **Missing entirely** | `badger.go`, `badger_test.go` |
| `pkg/store/redis` | 6.3 | **Missing entirely** | `redis.go`, `redis_test.go` |
| `pkg/vector/index.go` | 6.2 | Complete | - |
| `pkg/vector/conformance` | 17.2.2 | Complete | - |
| `pkg/vector/flat` | 6.2 | Complete | - |
| `pkg/vector/hnsw` | 6.2 | Complete | - |
| `pkg/embedding/provider.go` | 6.1 | Complete | - |
| `pkg/embedding/fake` | 17.1.1, 17.1.2 | Complete | - |
| `pkg/embedding/openai` | 6.1 | Complete | - |
| `pkg/embedding/ollama` | 6.1 | **Empty directory** | `ollama.go`, `ollama_test.go` |
| `pkg/cache/exact` | 5, 7 | Complete | - |
| `pkg/cache/semantic` | 5, 7 | Complete | - |
| `pkg/lineage` | 5, 8 | Complete | - |
| `pkg/cdc/listener.go` | 6.4 | Complete | - |
| `pkg/cdc/webhook` | 6.4 | Complete | - |
| `pkg/cdc/polling` | 6.4 | Complete | - |
| `pkg/cdc/nats` | 6.4 | **Missing entirely** | `nats.go`, `nats_test.go` |
| `pkg/reverb/client.go` | 11 | **Partial** | Background goroutines, CDC integration, options.go |
| `pkg/reverb/config.go` | 10 | Complete (struct) | YAML/env loading in main.go |
| `pkg/server/http.go` | 12 | **Near-complete** | `hit_rate` in stats |
| `pkg/server/grpc.go` | 13 | **Missing entirely** | `grpc.go`, `grpc_test.go`, `proto/reverb.proto` |
| `pkg/metrics/metrics.go` | 14.1 | **Stub** | Prometheus integration, all histograms/counters |
| `pkg/metrics/tracing.go` | 14.2 | **Stub** | OpenTelemetry integration |
| `cmd/reverb/main.go` | 18, 20 | **Minimal** | Config loading, factory construction |
| `test/integration/` | 17.4 | **Partial** | 4 of 6 test files missing |

---

## Recommended Implementation Order

Based on the design doc's Section 21 and dependency analysis:

1. **BadgerDB Store** — Unlocks durable caching (most impactful single gap)
2. **Background Goroutines** — Unlocks automatic invalidation and expiry
3. **Config Loading** — Makes the standalone server actually usable
4. **Ollama Provider** — Low effort, fills out embedding options
5. **gRPC Server + Proto** — Completes the API surface
6. **NATS CDC** — Completes CDC options
7. **Redis Store** — Completes storage backends
8. **Prometheus Metrics** — Production observability
9. **OpenTelemetry Tracing** — Distributed tracing
10. **Functional Options** — API ergonomics
11. **Missing Integration Tests** — Test coverage for new features
12. **Build Tags** — Clean up dependency tree
13. **Stats hit_rate + Metrics Collector wiring** — Minor polish
