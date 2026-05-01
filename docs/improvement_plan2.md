# Reverb — Product Improvement Plan (v2)

**Author:** Principal PM review
**Date:** 2026-04-30
**Codebase reviewed:** `main` @ `c9b6146` (post-0.1.0 unreleased)
**Audience:** core maintainers + prospective adopters (Go service teams, ML platform engineers, LLM-app builders, ops/SRE)

---

## 1. Executive summary

Reverb is in good shape as a **0.1.0 Go library** for a niche but valuable problem: **two-tier semantic response caching with knowledge-aware (lineage-based) invalidation** for LLM apps. The fundamentals are solid:

- Pluggable interfaces (`embedding.Provider`, `vector.Index`, `store.Store`, `cdc.Listener`)
- Three transports (Go API, HTTP REST, gRPC) plus experimental MCP JSON-RPC
- Production-grade ergonomics: OTel tracing, Prometheus metrics, per-tenant auth + rate limiting, embedding-pipeline concurrency cap, conformance test suites, BENCHMARKS.md with enforced false-positive budget

The differentiator vs. simpler LLM caches (e.g., LangChain `InMemoryCache`, GPTCache) is the **source-lineage invalidation engine**. Most teams who care about cached LLM answers eventually hit "my KB updated, but the cache is now lying" — and reverb solves that by design.

**The biggest gaps for adoption today are not in the core engine but in the surrounding surface:**

1. Only Go consumers can use the library directly; non-Go services have to hand-roll HTTP/gRPC clients.
2. There is no operator UX layer — no CLI, no admin UI, no pre-built Grafana dashboards. Engineers have to read the RUNBOOK to do anything.
3. The cache key surface (namespace + normalized prompt + model_id) doesn't accommodate common patterns: per-user caches, system-prompt-aware keys, tools/function schema versioning, multi-modal inputs, streaming responses.
4. Quality controls stop at threshold tuning — there's no re-ranker, no drift detection, no shadow-mode A/B against the live LLM.
5. Several adjacent integrations are missing despite the pluggable architecture: pgvector, Qdrant, Pinecone (vector); Postgres, S3 (store); LangChain/LlamaIndex/LiteLLM (framework adapters); OpenAI-API-compatible drop-in proxy mode.

This plan organizes ~40 concrete improvements across **8 categories**, each with target user, effort (S/M/L/XL), and priority (P0 = ship next; P1 = next quarter; P2 = opportunistic).

---

## 2. Categories at a glance

| # | Category | P0 | P1 | P2 | Theme |
|---|---|---|---|---|---|
| A | Multi-language SDKs & adapters | 2 | 2 | 1 | "Anyone can consume Reverb in 5 lines" |
| B | Operator UX (CLI, UI, dashboards) | 2 | 2 | 1 | "Operate it without reading the runbook" |
| C | Cache surface expansion | 1 | 4 | 2 | "Cache the things real LLM apps actually emit" |
| D | Cache quality & correctness | 1 | 3 | 2 | "Trust the hits" |
| E | Performance & scalability | 1 | 3 | 2 | "Scale beyond a single node" |
| F | Backend & integration ecosystem | 1 | 3 | 2 | "Drop into existing infra" |
| G | Security, compliance & multi-tenant | 1 | 2 | 2 | "Pass an enterprise security review" |
| H | Documentation, examples & known-gap closure | 2 | 1 | 1 | "Ship what the docs already promise" |

Total: **9 P0, 20 P1, 13 P2**.

---

## A. Multi-language SDKs & framework adapters

**Why this category matters first.** Reverb's core value proposition only manifests when applications integrate it. Today, only Go services can use it as a library; everyone else has to write their own HTTP/gRPC stubs and re-implement the lookup → LLM-call → store pattern. This is the single largest blocker to adoption outside the maintainer's own stack.

| ID | Item | Target user | Effort | Priority |
|---|---|---|---|---|
| A1 | **Python SDK** with `lookup`, `store`, `invalidate`, plus context-manager/decorator sugar (`@cached_completion`) that wraps OpenAI/Anthropic SDK calls. | LLM app devs (Python is dominant) | M | **P0** |
| A2 | **TypeScript/JS SDK** for Node + browser-edge runtimes (Vercel, Cloudflare Workers). Same surface as Python; ships as `@reverb/client`. | Edge/JS devs | M | **P0** |
| A3 | **LangChain & LlamaIndex integrations** — `ReverbCache` LLM cache class for LangChain; `ReverbCallback` for LlamaIndex. Closes the gap vs. GPTCache which already ships these. | Framework users | S (each) | P1 |
| A4 | **LiteLLM proxy hook** — register reverb as a LiteLLM cache plugin, so any model behind LiteLLM picks up reverb caching with one config line. | LLM gateway users | S | P1 |
| A5 | **Java + Rust SDKs** generated from the existing `.proto` plus thin sugar wrappers. Lower priority: targets infrastructure teams that have already adopted gRPC. | JVM / Rust services | M each | P2 |
| A6 | **OpenAPI 3.1 spec** for the HTTP API, published to repo + GitHub Pages. Enables auto-generated SDKs in 30+ languages and Swagger UI for try-it-now. | All HTTP consumers | S | **P0** (prereq for A1/A2 if we go OpenAPI-first) |

---

## B. Operator UX — CLI, admin UI, dashboards

**Why.** Today, an operator who wants to answer "what's my hit rate per namespace?" or "evict everything from source X" has to either curl `/v1/stats` and `/v1/invalidate` or use the Go client directly. RUNBOOK.md is excellent but every tool needs a UI before it scales beyond its author.

| ID | Item | Target user | Effort | Priority |
|---|---|---|---|---|
| B1 | **`reverb-cli` binary** (separate from server) with subcommands: `stats`, `lookup`, `store`, `invalidate <source>`, `evict --namespace`, `warm <jsonl>`, `export`, `import`, `validate-config`. Talks HTTP or gRPC. | Operators, on-call SRE | M | **P0** |
| B2 | **Admin web UI** (single-page, embedded in the binary at `/_admin`): hit-rate by namespace over time, top sources by entry count, entry browser with filter (namespace, model, age), test query box that runs a lookup and shows tier + similarity + lineage. | Operators, demo-driving devs | L | **P0** |
| B3 | **Pre-built Grafana dashboard JSON** committed to `dashboards/`: hit-rate, p50/p95/p99 lookup latency by tier, embedding errors, rate-limit rejections, queue depth, invalidation throughput. | Ops teams | S | P1 |
| B4 | **Pre-built Prometheus alerting rules** in `dashboards/alerts/`: hit-rate-drop, embedding-provider-failure-rate, vector-index-stale, store-unavailable. | Ops | S | P1 |
| B5 | **`reverb explain <entry-id>`** — surface entry's lineage tree, source content hashes, store/lookup history. Critical for debugging "why was this stale answer returned?". | Devs debugging hits | S | P2 |

---

## C. Cache surface expansion — what we can actually cache

**Why.** The current cache key is `(namespace, normalized prompt, model_id)`. Real LLM applications need richer keying *and* richer payloads.

| ID | Item | Problem | Solution | Target user | Effort | Priority |
|---|---|---|---|---|---|---|
| C1 | **Streaming response support** | Most modern LLM endpoints stream tokens (SSE / chunked). Reverb stores complete strings only — caller can't replay a cached answer as a stream. | Add `chunks []ResponseChunk` (delta + finish_reason) alongside `ResponseText`. New HTTP endpoint `POST /v1/lookup-stream` returns SSE if cached. | LLM app devs | M | **P0** |
| C2 | **Configurable cache-key components** | Two callers with different system prompts or different `tools` schemas get the same response back. Today only `model_id` is part of the key. | Add `KeyExtras map[string]string` to `LookupRequest`/`StoreRequest` — operators choose which to fold into the hash (`system_prompt`, `tools_hash`, `user_id`, `locale`, `kb_version`). Also expose `WithKeyExtractor` option for the Go API. | Library users | M | P1 |
| C3 | **Multi-modal prompt support** | Image/audio prompts can't be cached; embedding/normalize layer assumes text. | Define `Prompt` as a typed message-list with parts (`text`, `image_url`, `image_b64`, `audio`). Allow plug-in multi-modal embedders (CLIP, OpenAI's `text-embedding-3-large` with image input, Cohere `embed-multilingual-v3`). | RAG/multi-modal app devs | L | P1 |
| C4 | **Tool / function-call result caching** | `ResponseMeta map[string]string` is too thin to capture function-call args + results. | Promote response to a structured `Response` shape with `Text`, `ToolCalls []ToolCall`, `FinishReason`. Lookup respects schema-version bump as auto-invalidation. | Agent builders | M | P1 |
| C5 | **OpenAI-compatible reverse-proxy mode** | Standard adoption pattern is "drop a cache in front of OpenAI." We have no `POST /v1/chat/completions` shim today. | New mode `--proxy openai --upstream https://api.openai.com`: forward on miss, cache on success, return on hit. Honour `cache-control: no-cache` request header for bypass. | Anyone using OpenAI-API-shaped servers (incl. vLLM, llama.cpp, Together, Anyscale, Ollama, OpenRouter) | L | **P1 (high leverage)** |
| C6 | **Negative caching (miss-known)** | If the LLM consistently returns "I don't know", we re-pay every time. | Optional `StoreNegative` flag with shorter TTL (e.g., 5min) so we cache "no answer" and skip the LLM for repeats — but with auto-expiry so retry happens after KB update. | Cost-sensitive teams | S | P2 |
| C7 | **Per-namespace config (TTL, threshold, scope)** | Currently global. A "support-bot" namespace may want 24h TTL + 0.97; a "scratchpad" may want 5min + 0.85. | YAML schema: `namespaces: { name: x, config: { default_ttl, similarity_threshold, scope_by_model } }`. Falls back to global. | Multi-tenant operators | S | P1 |
| C8 | **Sliding TTL on hit** | Today TTL is fixed at write. A frequently hit entry expires the same as a cold one. | Optional `RefreshOnHit time.Duration` extending `ExpiresAt` on lookup. Implementation note: do under same atomic update as `IncrementHit`. | Library users | S | P2 |
| C9 | **Cache-control hint API** | Caller has no way to say "force miss" or "store-only-if-confident". | Add `Lookup(req, ...HintOpt)` opts: `Bypass()`, `MinSimilarity(f)`, `ReadOnly()`. Mirror in HTTP via `X-Reverb-Cache-Control` header. | Library users | S | P2 |

---

## D. Cache quality & correctness

**Why.** A cache that returns wrong answers is worse than no cache. Reverb already enforces a published false-positive budget (great!), but quality tooling is otherwise ad-hoc.

| ID | Item | Problem | Solution | Target user | Effort | Priority |
|---|---|---|---|---|---|---|
| D1 | **Cross-encoder re-ranker tier** | Cosine similarity is approximate; even at 0.95 a few false positives slip through. | Optional Tier 2.5: after vector top-k, run a small cross-encoder (e.g., `bge-reranker-base`, ~30ms) to re-score; require both cosine ≥ T₁ *and* rerank ≥ T₂. Pluggable via new `Reranker` interface. | Quality-sensitive teams | M | **P0** |
| D2 | **Adaptive similarity threshold** | Operators pick 0.95 once and never revisit. Different namespaces / embedders need different thresholds. | Background job samples random hit pairs, asks an LLM judge "are these the same intent?" → suggests threshold per namespace. Surface as `reverb-cli suggest-thresholds`. | Researchers, ML platform | M | P1 |
| D3 | **Drift detection across model versions** | When `gpt-4o` → `gpt-4.1`, cached answers may no longer reflect what the live model would say. | Track `(model_id, model_version_pinned_at)` per entry. Optional shadow-mode: on hit, with probability p, also call live LLM and emit divergence metric. Auto-invalidate when divergence exceeds threshold. | Teams running model upgrades | L | P1 |
| D4 | **Hit-quality feedback loop** | No way for downstream users to flag "this cached answer was wrong." | New endpoint `POST /v1/entries/{id}/feedback` with `+1/-1` and optional reason. Negative feedback above threshold auto-invalidates; both signals feed quality dashboards. | App devs, support-bot operators | M | P1 |
| D5 | **Per-source freshness SLOs** | We don't know how stale a source's entries are allowed to be. | Add `freshness_ttl` per source-id (separate from entry TTL). Entries older than this become "stale-but-served" with a header/metadata flag, letting callers decide. | Operators of long-lived KBs | S | P2 |
| D6 | **Consistency mode** flag on `LookupRequest` | Some callers can tolerate stale; others can't. | Modes: `eventually` (default, current behavior), `lineage_fresh` (fail lookup if any source CDC event is in invalidation queue), `live_only` (always miss). | Library users | S | P2 |

---

## E. Performance & scalability

**Why.** The current model assumes single-node deployment with in-memory vector index. Several common scaling pain points are not yet addressed.

| ID | Item | Problem | Solution | Target user | Effort | Priority |
|---|---|---|---|---|---|---|
| E1 | **Singleflight on miss** | If 100 callers hit a cold cache simultaneously for the same prompt, all 100 hit the LLM. | In `Client.Lookup`, on miss, return a "miss-token" the caller passes back into `Store`; or expose a `LookupOrCall(ctx, req, fillFn)` helper that coalesces concurrent fills via `golang.org/x/sync/singleflight`. | All users (cost saver) | S | **P0** |
| E2 | **Persistent vector index (HNSW-on-disk)** | HNSW is memory-only; restart loses the index unless `rebuild_vector_index_on_startup=true` re-scans the whole store (O(N)). | Add a `vector.PersistableIndex` variant that periodically snapshots HNSW graph to disk; load on startup. Alternatively integrate `pgvector` or `qdrant` (see F-section). | Operators with large caches | M | P1 |
| E3 | **Horizontal cluster mode** (read-through replicas) | One reverb node per service is the only deployment shape today. | Cluster: shared Redis/Postgres store; each node owns its own vector index but listens on shared CDC; gossip-style invalidation broadcast. Define `cluster.Membership` interface. | High-throughput services | XL | P2 |
| E4 | **Bounded eviction policy** | Only TTL-based eviction exists. A namespace can grow unboundedly. | Add `max_entries_per_namespace` + `eviction_policy: lru | lfu | size`. Track `LastHitAt` (already there) and add bookkeeping. | Operators with bursty traffic | M | P1 |
| E5 | **Background embedding-fill** | Entries with `EmbeddingMissing=true` (embed failed during Store) never get retried; they're permanently exact-only. | Reaper-adjacent goroutine periodically picks up `EmbeddingMissing` entries and retries the embedder; on success, populates `Embedding` and adds to vector index. | Library users | S | P1 |
| E6 | **Batch lookup / batch store APIs** | High-throughput callers (offline eval pipelines) want amortized RTT. | New `POST /v1/lookup-batch` and `BatchLookup(ctx, reqs []LookupRequest)`. Batches embedder calls where possible (OpenAI embedding API supports batch). | Eval pipelines, batch jobs | S | P2 |
| E7 | **Embedding-result caching** | Identical normalized prompts re-embed every time. | Tiny LRU on `Embedder` wrapper (already have `pkg/embedding/throttled` — extend with optional in-process LRU). | Library users | S | P2 |

---

## F. Backend & integration ecosystem

**Why.** The pluggable interface design is the right call, but the set of *shipped* implementations doesn't yet match what teams have in their stack.

| ID | Item | Problem | Solution | Target user | Effort | Priority |
|---|---|---|---|---|---|---|
| F1 | **Postgres / pgvector store + index** | Many teams already run Postgres; pgvector unifies storage and ANN. Currently we'd need two separate adapters. | Single adapter `pkg/store/postgres` + `pkg/vector/pgvector` sharing a connection pool. Schema migration shipped. | Postgres-first teams | M | **P0** |
| F2 | **Qdrant vector backend** | Qdrant is the most popular dedicated vector DB in 2026; many teams already operate it. | `pkg/vector/qdrant`. Lineage filter pushed down as Qdrant payload filter for fast scoped invalidation. | Qdrant users | M | P1 |
| F3 | **Pinecone vector backend** | Same rationale as Qdrant for managed/cloud teams. | `pkg/vector/pinecone`. | Pinecone users | M | P1 |
| F4 | **Weaviate / Milvus** | Round out the "big four" vector DBs. | Two more adapters; share scaffolding from F2/F3. | | M each | P2 |
| F5 | **S3 / GCS object-store backend for cold tier** | Long-tail entries that rarely get hit waste in-memory or Redis space. | Tiered store: hot in Redis/Badger, cold in S3 with lazy promote. New `store.TieredStore` composing two sub-stores. | Cost-sensitive | M | P2 |
| F6 | **Kafka / Redpanda CDC listener** | NATS is good but Kafka dominates large-enterprise event buses. | `pkg/cdc/kafka`, sharing event-shape with NATS. | Enterprise | S | P1 |
| F7 | **Polling CDC: expose in cmd config** | The `polling` listener exists but the standalone binary's `cdc.mode` switch only supports `"webhook"` and `"nats"` — README/COMPATIBILITY claims polling is "library-only" but there's no architectural reason for that. | Add `case "polling"` in `newCDCListener` in `cmd/reverb/main.go`; add `poll_interval` + source-list config. | Operators with no event bus | S | P1 |

---

## G. Security, compliance & multi-tenant maturity

**Why.** Reverb already has bearer-token auth, multi-tenant scoping, rate limiting, and TLS-aware listeners — better than most early-stage caches. The remaining gaps are the ones an enterprise security reviewer will flag.

| ID | Item | Problem | Solution | Target user | Effort | Priority |
|---|---|---|---|---|---|---|
| G1 | **PII redaction hook in normalize pipeline** | `normalize.Normalize` only handles NFC/case/whitespace. Sensitive PII gets hashed and stored unredacted in `PromptText`. | Optional `Redactor` interface invoked between normalize and hash; ship a default regex-based redactor (emails, phones, credit-card patterns, SSN). Per-namespace toggle. | Regulated industries | M | **P0** |
| G2 | **JWT/OIDC auth** | API keys are static in YAML — no rotation story. | Pluggable `auth.Verifier`: bearer-key (current), JWT (RS256 / ES256 with JWKS endpoint), OIDC (introspect). Gives token-rotation for free. | Enterprise | M | P1 |
| G3 | **mTLS support on HTTP/gRPC** | Today TLS is plain bearer-token over HTTPS-by-reverse-proxy. mTLS would be in-process. | `server.tls: { cert, key, client_ca }` config. | High-trust zones | S | P1 |
| G4 | **API key rotation tooling** | Rotating a tenant key today means edit YAML + reload. No overlap window. | Allow `api_keys: []string` per tenant (already supported!) plus a `expires_at` per key. Stop accepting expired keys. Surface keys-expiring-soon metric. | Multi-tenant operators | S | P2 |
| G5 | **Audit log of admin operations** | `Invalidate`, `DeleteEntry`, manual key changes leave no record beyond OTel spans (which most teams don't archive). | Append-only audit log (sink: file, syslog, or webhook) with actor (tenant), action, target, timestamp. | Compliance | S | P2 |
| G6 | **Data-retention / right-to-be-forgotten endpoint** | A user requesting GDPR deletion: today operators have to scan namespaces and delete by hand. | `POST /v1/erase { match: { user_id: "..." } }` matched against `KeyExtras` (depends on C2). | GDPR-bound services | S | P2 — depends on C2 |

---

## H. Documentation, examples & known-gap closure

**Why.** Docs are unusually strong for a project this young (RUNBOOK, COMPATIBILITY, BENCHMARKS), but several gaps are explicitly tracked in the docs themselves and should be closed before 1.0.

| ID | Item | Notes | Effort | Priority |
|---|---|---|---|---|
| H1 | **Close: metrics HTTP server "not yet wired"** | README/COMPATIBILITY/CHANGELOG all flag this as a known gap. Looking at `cmd/reverb/main.go` it actually *is* wired now (`WithMetricsOnMux` + `NewMetricsServer`). **The doc is stale.** Fix-forward: sweep all three docs and the `metrics_test.go` invariants. | S | **P0** |
| H2 | **Implement `--validate` flag** | COMPATIBILITY.md upgrade-testing checklist references this; CHANGELOG known-gaps repeats it. Should parse config + run a sample lookup + exit. | S | **P0** |
| H3 | **Move MCP from experimental → beta** | The MCP wrapper at `pkg/server/mcp` is solid (570-line implementation + 470 lines of tests). Define the contract and graduate. | S | P1 |
| H4 | **Recipe-style example: caching a real OpenAI chat app** | All current examples use `fake.New(64)` so users only ever see exact hits. Add `examples/openai-chat/` with real OpenAI key + visible semantic hits. (README already calls out this gap.) | S | **P0** |
| H5 | **Recipe-style example: reverb behind LangChain** | Demonstrate the LangChain integration (A3) with a 30-line script. | S | P1 — depends on A3 |
| H6 | **Capacity-planning calculator** in RUNBOOK | A spreadsheet/script that takes (req/sec, prompt-len, embedding-dim, expected hit-rate) and outputs (memory, RPS budget per backend, $/month). | S | P2 |

---

## 3. Suggested sequencing (next 3 quarters)

### Quarter 1 — "Make it adoptable outside the maintainer's stack"
**Theme: SDKs + operability + close known gaps. P0s only.**
- A1, A2, A6 (Python + JS SDKs + OpenAPI spec)
- B1, B2 (CLI + Admin UI)
- C1 (streaming response support)
- C5 (OpenAI-compatible proxy mode — high leverage even though it's listed P1)
- D1 (cross-encoder re-ranker)
- E1 (singleflight)
- F1 (pgvector / Postgres backend)
- G1 (PII redaction hook)
- H1, H2, H4 (close stale-doc gap, implement `--validate`, real-OpenAI example)

**Net result:** Reverb 0.2 ships as a polyglot, pluggable-into-real-stacks library with materially better quality controls and operator UX. Story for the README becomes "drop in front of OpenAI in 5 lines, in any language, with KB-aware invalidation."

### Quarter 2 — "Beat GPTCache on integrations + quality"
**Theme: framework adapters + advanced quality + horizontal scaling foundations.**
- A3, A4 (LangChain, LlamaIndex, LiteLLM)
- B3, B4 (dashboards + alerts)
- C2, C3, C4, C7 (configurable key, multi-modal, tool-calls, per-namespace config)
- D2, D3, D4 (adaptive thresholds, drift detection, feedback loop)
- E2, E4, E5 (persistent vector index, eviction policy, embedding backfill)
- F2, F3, F6, F7 (Qdrant, Pinecone, Kafka CDC, polling-in-cmd)
- G2, G3 (JWT/OIDC, mTLS)
- H3, H5 (graduate MCP, framework example)

### Quarter 3 — "Enterprise-ready 1.0"
**Theme: hardening + ecosystem completeness + GA.**
- A5 (Java / Rust)
- C6, C8, C9 (negative cache, sliding TTL, cache-control hints)
- D5, D6 (per-source freshness, consistency modes)
- E3, E6, E7 (cluster mode, batch APIs, embedding result cache)
- F4, F5 (Weaviate / Milvus, S3 cold tier)
- G4, G5, G6 (key rotation, audit log, GDPR endpoint)
- H6 (capacity calculator)
- **Cut 1.0** — freeze public API, follow COMPATIBILITY.md semver going forward.

---

## 4. What I deliberately did **not** propose

Worth naming, because the absence might surprise:

- **A built-in LLM gateway / completion router.** Tempting to merge with reverb so callers don't even have to write the "on miss, call LLM" path. But this overlaps with LiteLLM, Portkey, OpenRouter, etc., and would dilute reverb's identity as a *cache*. Better to integrate via A4.
- **A cloud SaaS offering.** No signal in the codebase that maintainers want to operate hosted reverb. Plan stays self-host-first.
- **GraphQL transport.** Three transports is already a maintenance tax. Skip.
- **A custom embedding model.** OpenAI / Ollama / pluggable is the right shape; reverb shouldn't ship its own embedder.
- **Migrating off Go.** The Go choice is load-bearing for performance + single-binary deploy. Don't relitigate.

---

## 5. Metrics to watch (so we know if any of this works)

Per-quarter OKRs to attach to this plan:

| Metric | Today (estimate) | Q1 target | Q2 target | Q3 target |
|---|---|---|---|---|
| GitHub stars | <200 | 1k | 3k | 10k |
| Non-Go production deployments | 0 | 5 | 25 | 100 |
| 3rd-party backend adapters merged | 0 | 1 (pgvector) | 4 | 6 |
| Median time-to-first-hit (new user, install→first cached response) | 30 min (hand-rolled HTTP) | 5 min (Python SDK) | 2 min (LangChain one-liner) | 1 min |
| False-positive rate at default threshold | 0/10 (current budget) | 0/100 (with reranker D1) | 0/1000 | 0/1000 |
| `/metrics` adoption rate (% of deploys exposing it) | unknown — fix doc first | 80% | 95% | 95% |

---

## 6. Risks & open questions for the maintainers

1. **Are SDKs in scope, or do we expect users to consume HTTP/gRPC directly?** Plan assumes yes. If no, A1/A2 drop and adoption growth is structurally capped.
2. **Is the maintainer team open to a small frontend (B2 admin UI)?** Adds a JS/TS toolchain to the repo. If "Go-only repo" is a hard constraint, fall back to a separate `reverb-ui` repo.
3. **Cluster mode (E3) is XL and may distort the codebase.** Worth a separate design doc before committing.
4. **Are we OK with optional ML dependencies (D1 cross-encoder)?** ONNX runtime is the cleanest path; needs CGO or a Go-native runtime. Should be opt-in build tag.
5. **Compatibility budget.** Many of these (C2, C3, C4) change public types. They should land before 1.0 cut, behind compatibility flags if needed.

---

*End of plan. Open to discussion on category prioritization, especially the C5 (OpenAI proxy mode) and D1 (re-ranker) calls — both could justifiably be P0 instead of P1.*
