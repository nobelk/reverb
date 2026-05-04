# Roadmap

This roadmap is organized into four phases — **Foundation** (already shipped), **Now**, **Next**, and **Later** — with each phase broken into small, independently shippable steps. Each step lists what it does, why it matters, and what it unblocks.

The Now/Next/Later sequencing is derived from `improvement_plan2.md`. The Later phase additionally includes the optional LLM-gateway mode endorsed in the project's constitutional discussions.

When this roadmap conflicts with `mission.md`, the mission wins.

---

## Phase 0 — Foundation (shipped)

The 0.1.0 baseline. Listed here so the roadmap is honest about what is *not* future work.

- Two-tier cache: SHA-256 exact match + embedding-similarity semantic match.
- Source-lineage tracking and CDC-driven invalidation.
- Three storage backends (memory, Redis, BadgerDB), two vector indices (flat, HNSW), three embedding providers (fake, OpenAI, Ollama), three CDC listeners (webhook, polling, NATS). Note: the `polling` listener exists in the library only; the standalone binary's `cdc.mode` switch does not yet expose it. Wiring it through `cmd/reverb` is tracked as 2.18.
- HTTP REST and gRPC servers; experimental MCP JSON-RPC.
- Per-tenant bearer-token auth, rate limiting, OpenTelemetry tracing, Prometheus metrics surface.
- Conformance test suites for `store.Store` and `vector.Index`.
- Published latency baselines and an enforced false-positive budget.

---

## Phase 1 — Now

**Theme:** Make Reverb adoptable outside the maintainer's own stack.

The library is solid; the surface around it is thin. Phase 1 closes the highest-leverage adoption gaps and delivers the documentation already promised.

### 1.1 Publish OpenAPI 3.1 spec for the HTTP API

- **What:** Author a complete OpenAPI 3.1 document for `/v1/*` endpoints. Publish to the repo and GitHub Pages.
- **Why:** Prerequisite for SDK generation in any language; enables Swagger UI and try-it-now flows.
- **Unblocks:** 1.2, 1.3, 2.1, 2.2.

### 1.2 Python SDK

- **What:** A `reverb` PyPI package wrapping `lookup`, `store`, `invalidate`, plus a `@cached_completion` decorator that wraps OpenAI/Anthropic SDK calls.
- **Why:** Python is the dominant language for LLM apps. Until Python users can `pip install` Reverb, adoption outside Go is structurally capped.
- **Depends on:** 1.1.

### 1.3 TypeScript / JavaScript SDK

- **What:** `@reverb/client` for Node and edge runtimes (Vercel, Cloudflare Workers). Same surface as the Python SDK.
- **Why:** Edge and Node deployments are a sizable share of LLM-app traffic.
- **Depends on:** 1.1.

### 1.4 `reverb-cli` operator binary

- **What:** A separate binary (not bundled into the server) with subcommands `stats`, `lookup`, `store`, `invalidate <source>`, `evict --namespace`, `warm <jsonl>`, `export`, `import`, `validate-config`. Talks HTTP or gRPC.
- **Why:** Operators today must `curl` the API or write Go code. A CLI is the minimum operator UX.

### 1.6 Streaming response support

- **What:** Add `chunks []ResponseChunk` (delta + finish_reason) alongside `ResponseText`. New endpoint `POST /v1/lookup-stream` returns SSE if cached.
- **Why:** Modern LLM endpoints stream tokens. Reverb today stores complete strings only, so callers cannot replay a cached answer as a stream.

### 1.7 OpenAI-compatible reverse-proxy mode

- **What:** New mode `--proxy openai --upstream https://api.openai.com`: forward on miss, cache on success, return on hit. Honors `cache-control: no-cache` for bypass.
- **Why:** "Drop a cache in front of OpenAI" is the dominant adoption pattern. This unlocks all OpenAI-API-shaped servers — vLLM, llama.cpp, Together, Anyscale, Ollama, OpenRouter — with one flag.
- **Sequencing note:** Listed as P1 in `improvement_plan2.md` (item C5) but promoted to Phase 1 here because of leverage: a single flag unlocks every OpenAI-API-shaped upstream, which is the highest-leverage adoption surface in the roadmap. The plan's own §3 Q1 list calls this promotion out explicitly.

### 1.8 Cross-encoder re-ranker tier

- **What:** Optional Tier 2.5 between vector top-k and the hit decision. After cosine similarity, run a small cross-encoder (e.g., `bge-reranker-base`) and require both cosine ≥ T₁ and rerank ≥ T₂. Pluggable via a new `Reranker` interface.
- **Why:** Cosine alone leaks false positives at high thresholds. The reranker tightens the false-positive budget toward 0/100.
- **Constraint:** Per `tech-stack.md`, ML-runtime dependencies live behind a build tag. Default builds remain CGO-free.

### 1.9 Singleflight on cache miss

- **What:** Coalesce concurrent fills via `golang.org/x/sync/singleflight`, exposed as `LookupOrCall(ctx, req, fillFn)`.
- **Why:** Today a cold-cache thundering herd hits the LLM N times for the same prompt. Singleflight is a one-line cost saver for every user.

### 1.10 Postgres / pgvector backend

- **What:** A single adapter `pkg/store/postgres` + `pkg/vector/pgvector` sharing a connection pool. Schema migrations shipped.
- **Why:** Many teams already run Postgres; pgvector unifies storage and ANN. The first community-requested backend.

### 1.11 PII redaction hook in normalize pipeline

- **What:** Optional `Redactor` interface invoked between normalize and hash. Ships a default regex-based redactor (emails, phones, credit-card patterns, SSN). Per-namespace toggle.
- **Why:** `normalize.Normalize` only handles NFC/case/whitespace today; PII gets hashed and stored unredacted. Required for regulated-industry adoption.

### 1.12 Documentation sweep — close known stale claims

- **What:** Sweep README, COMPATIBILITY, and CHANGELOG to remove the "metrics HTTP server not yet wired" caveat — it is wired now via `WithMetricsOnMux` + `NewMetricsServer`. Add metrics-related invariants to tests.
- **Why:** Stale "known gaps" in docs damage trust more than the gaps themselves.

### 1.13 Implement `--validate` flag in `cmd/reverb`

- **What:** `reverb --validate --config foo.yaml` parses the config, runs a sample lookup, and exits with non-zero on any failure.
- **Why:** Referenced in `COMPATIBILITY.md` and the upgrade-testing checklist; currently unimplemented.

### 1.14 `examples/openai-chat/` with real semantic hits

- **What:** A self-contained example that uses a real OpenAI key (or Ollama for offline testing) and demonstrates a semantic hit on a paraphrased prompt.
- **Why:** Every existing example uses the deterministic `fake` embedder, so users only ever see exact hits. The semantic-cache value proposition is invisible until they switch embedders themselves.

### Phase 1 exit criteria

- Python and TypeScript SDKs published; OpenAPI spec on GitHub Pages.
- `reverb-cli` shipped. (Admin UI moved to Phase 2 — see §2.24.)
- Streaming, reverse-proxy, re-ranker, and singleflight available behind opt-in flags or new APIs.
- pgvector backend merged with conformance compliance.
- All "known gaps" called out in current docs are either fixed or explicitly removed from the doc.

---

## Phase 2 — Next

**Theme:** Beat the field on integrations, quality, and scale.

Phase 1 made Reverb adoptable. Phase 2 makes it the obvious choice over GPTCache, LangChain `InMemoryCache`, and ad-hoc Redis caches.

### 2.1 Framework adapters: LangChain, LlamaIndex

- **What:** A `ReverbCache` LLM-cache class for LangChain; a `ReverbCallback` for LlamaIndex.
- **Why:** Closes the parity gap with GPTCache, which already ships these.
- **Depends on:** 1.2.

### 2.2 LiteLLM proxy hook

- **What:** Register Reverb as a LiteLLM cache plugin so any model behind LiteLLM picks up Reverb caching with one config line.
- **Why:** LiteLLM has become the default gateway for many teams; this is the one-line integration story.
- **Depends on:** 1.2.

### 2.3 Pre-built Grafana dashboard JSON

- **What:** Dashboards committed to `dashboards/` covering hit-rate, p50/p95/p99 latency by tier, embedding errors, rate-limit rejections, queue depth, invalidation throughput.

### 2.4 Pre-built Prometheus alerting rules

- **What:** Alert rules in `dashboards/alerts/` for hit-rate drop, embedding-provider failure, vector-index staleness, store-unavailable.

### 2.5 Configurable cache-key components

- **What:** `KeyExtras map[string]string` on `LookupRequest`/`StoreRequest`; operators choose which to fold into the hash (`system_prompt`, `tools_hash`, `user_id`, `locale`, `kb_version`). `WithKeyExtractor` option for the Go API.
- **Why:** Today only `model_id` is in the key, so callers with different system prompts or tool schemas collide. Foundational for any per-user or version-aware caching.
- **Unblocks:** 3.7 (GDPR erase endpoint).

### 2.6 Multi-modal prompt support

- **What:** `Prompt` becomes a typed message-list with `text`, `image_url`, `image_b64`, `audio` parts. Multi-modal embedders (CLIP, OpenAI image inputs, Cohere multilingual) plug in via the existing `Provider` interface.
- **Why:** Image and audio prompts are increasingly common and currently uncacheable.
- **Compatibility note:** Reshapes a public type in `pkg/reverb`. Per `mission.md` principle 4, breaking the public API requires a major version. This item must land in the 0.x window, before the 3.16 1.0 cut; it cannot be deferred into 1.x without a 2.0 bump.

### 2.7 Tool / function-call result caching

- **What:** Promote response to a structured shape with `Text`, `ToolCalls []ToolCall`, `FinishReason`. Schema-version bumps auto-invalidate.
- **Why:** Agent builders cache tool calls today by stuffing JSON into `ResponseMeta`; that surface is too thin.
- **Compatibility note:** Reshapes a public type in `pkg/reverb` (and follows on from the streaming-chunks addition in 1.6). Same constraint as 2.6: must land in the 0.x window before the 3.16 1.0 cut.

### 2.8 Per-namespace configuration

- **What:** YAML schema `namespaces: { name: x, config: { default_ttl, similarity_threshold, scope_by_model } }` with fall-through to global defaults.
- **Why:** A "support-bot" namespace and a "scratchpad" namespace need different TTLs and thresholds; today everything is global.

### 2.9 Adaptive similarity threshold

- **What:** Background job samples random hit pairs, asks an LLM judge whether intents match, and suggests a per-namespace threshold. Surface as `reverb-cli suggest-thresholds`.

### 2.10 Drift detection across model versions

- **What:** Track `(model_id, model_version_pinned_at)` per entry. Optional shadow-mode: with probability *p* on hit, also call the live LLM and emit a divergence metric. Auto-invalidate when divergence exceeds threshold.
- **Why:** When `gpt-4o` becomes `gpt-4.1`, cached answers may no longer reflect the live model.

### 2.11 Hit-quality feedback loop

- **What:** `POST /v1/entries/{id}/feedback` with `+1/-1` and optional reason. Negative feedback above a threshold auto-invalidates; both signals feed quality dashboards.

### 2.12 Persistent vector index (HNSW snapshot)

- **What:** A `vector.PersistableIndex` variant that periodically snapshots the HNSW graph to disk and loads it on startup.
- **Why:** Today HNSW is memory-only; restart loses the index unless `rebuild_vector_index_on_startup=true` re-scans the whole store (O(N)).

### 2.13 Bounded eviction policy

- **What:** `max_entries_per_namespace` + `eviction_policy: lru | lfu | size`.
- **Why:** Only TTL-based eviction exists today; a hot namespace can grow unboundedly.

### 2.14 Background embedding-fill reaper

- **What:** A goroutine that periodically picks up entries with `EmbeddingMissing=true` and retries the embedder.
- **Why:** Today such entries are permanently exact-only.

### 2.15 Qdrant vector backend

- **What:** `pkg/vector/qdrant`. Lineage filter pushed down as a Qdrant payload filter for fast scoped invalidation.

### 2.16 Pinecone vector backend

- **What:** `pkg/vector/pinecone`.

### 2.17 Kafka / Redpanda CDC listener

- **What:** `pkg/cdc/kafka` sharing the event shape with NATS.

### 2.18 Polling CDC in the standalone binary

- **What:** Add `case "polling"` in `newCDCListener` in `cmd/reverb/main.go`; add `poll_interval` and source-list config.
- **Why:** The polling listener already exists in the library; the binary just doesn't expose it. Trivial fix-forward.

### 2.19 JWT / OIDC auth

- **What:** Pluggable `auth.Verifier`: bearer-key (current), JWT (RS256/ES256 with JWKS), OIDC introspect. Token rotation comes for free.

### 2.20 mTLS support on HTTP and gRPC

- **What:** `server.tls: { cert, key, client_ca }` config so TLS terminates in-process rather than relying on a reverse proxy.

### 2.21 Graduate MCP from experimental to beta

- **What:** Move `pkg/server/mcp` from experimental to beta-track per the contract referenced from `tech-stack.md`. Document supported JSON-RPC methods and publish the wrapper's stability promise.
- **Graduation criteria (all must hold at graduation):**
  1. **Method coverage frozen.** A documented list of supported MCP JSON-RPC methods exists in `pkg/server/mcp/README.md`; methods outside the list explicitly return `method not found`.
  2. **Test coverage.** ≥85% line coverage in `pkg/server/mcp`, with at least one test per documented method covering the success path and one error path.
  3. **No breaking changes for two consecutive minor releases.** The supported-methods list and request/response shapes do not change across two minor versions before graduation; CI guards this with a JSON-schema diff against a checked-in golden file.
  4. **Soak window.** ≥30 days running on the standalone binary in at least one production deployment, with no MCP-specific incident reports filed.
  5. **Conformance against MCP spec.** The wrapper passes whatever upstream MCP conformance tooling is available at graduation time; if none exists, that fact is documented and revisited at the next minor release.
- **On graduation:** `tech-stack.md` "Public surface" row for MCP is updated from beta-track to a stability-promised surface (additive changes only within a major version), and the link from `tech-stack.md` to this item is replaced with the new contract.

### 2.22 `examples/langchain-reverb/`

- **What:** A 30-line script demonstrating Reverb behind LangChain.
- **Depends on:** 2.1.

### 2.23 `reverb explain <entry-id>` debugging command

- **What:** New `reverb-cli` subcommand that prints, for a given entry: its lineage tree (sources and their content hashes), the store/lookup history (created-at, hits, last-hit-at, last-invalidation-cause), the embedding-tier metadata (provider, model, dimension, `EmbeddingMissing` flag), and any per-namespace config that applied at lookup time.
- **Why:** Pairs with the dashboards in 2.3 and the alerts in 2.4 — when an alert fires or a dashboard shows an anomaly, an operator needs an entry-level "why was this returned?" view. The admin UI's test-query box (2.24) shows tier + similarity + lineage for a *new* lookup; this is the equivalent for an *existing* entry that's already in the cache.
- **Depends on:** 1.4.

### 2.24 Admin web UI at `/_admin`

- **What:** Single-page UI surfaced by the standalone binary. Hit-rate by namespace over time, top sources by entry count, an entry browser with filters, and a test-query box that runs a `lookup` and shows tier + similarity + lineage.
- **Why:** Demos, debugging, and on-call workflows all benefit. A graphical surface complements the dashboards in 2.3 and the alerts in 2.4 — the UI is where an on-call jumps after the alert fires and the dashboard narrows the suspect namespace.
- **Constraint:** Lives in a sibling `reverb-ui` repo to preserve the Go-only main-repo invariant in `tech-stack.md` §"Repository composition". The main-repo standalone binary embeds the built static asset bundle via `embed.FS` and exposes it behind a `WithAdminUI()` option that ships disabled-by-default per `mission.md` principle 4.
- **Sequencing note:** Originally listed as 1.5 in this roadmap; deferred to Phase 2 in the spec at `specs/2026-04-30-adoption-surface/`. The CLI (1.4) ships in Phase 1 alone; the graphical surface follows once dashboards (2.3) and alerts (2.4) give it neighboring tools to compose with.
- **Depends on:** 1.1 (OpenAPI contract that the UI calls).

- Reverb integrates with LangChain, LlamaIndex, and LiteLLM via published adapters.
- Operators inherit dashboards and alerts; no Grafana boards are hand-rolled.
- Cache key surface accommodates per-user, per-version, and multi-modal prompts.
- Quality feedback loop (D2/D3/D4) measurably tightens the false-positive budget toward 0/1000.
- Qdrant and Pinecone backends shipped; Kafka CDC listener available.
- JWT/OIDC and mTLS available; MCP graduated to beta.

---

## Phase 3 — Later (toward 1.0)

**Theme:** Enterprise readiness, ecosystem completeness, and the optional gateway. Cut 1.0 at the end of this phase.

### 3.1 Java and Rust SDKs

- **What:** SDKs generated from the existing `.proto`, with thin language-idiomatic sugar wrappers.
- **Why:** Closes the polyglot story for JVM and Rust services. Lower priority than Python/JS because these audiences typically already speak gRPC.

### 3.2 Negative caching

- **What:** Optional `StoreNegative` flag with shorter TTL (e.g., 5 min) so "I don't know" responses skip the LLM for repeats and auto-expire.

### 3.3 Sliding TTL on hit

- **What:** Optional `RefreshOnHit time.Duration` extending `ExpiresAt` on lookup. Updated atomically with `IncrementHit`.

### 3.4 Cache-control hint API

- **What:** `Lookup(req, ...HintOpt)` opts: `Bypass()`, `MinSimilarity(f)`, `ReadOnly()`. Mirror in HTTP via `X-Reverb-Cache-Control` header.

### 3.5 Per-source freshness SLOs and consistency modes

- **What:** `freshness_ttl` per source-id; consistency modes on `LookupRequest` — `eventually` (default), `lineage_fresh`, `live_only`.

### 3.6 Horizontal cluster mode

- **What:** Shared Redis/Postgres store; each node owns its own vector index but listens on shared CDC; gossip-style invalidation broadcast. Define `cluster.Membership` interface.
- **Caveat:** The largest item in the roadmap by far. Per `improvement_plan2.md §6`, a separate design doc is required before committing.

### 3.7 GDPR / right-to-be-forgotten endpoint

- **What:** `POST /v1/erase { match: { user_id: "..." } }` matched against `KeyExtras`.
- **Depends on:** 2.5.

### 3.8 Audit log of admin operations

- **What:** Append-only audit log (sink: file, syslog, or webhook) with actor, action, target, timestamp.

### 3.9 API key rotation tooling

- **What:** Per-key `expires_at`; metric for keys-expiring-soon; refusal of expired keys with overlap window.

### 3.10 Batch lookup and store APIs

- **What:** `POST /v1/lookup-batch` and `BatchLookup(ctx, reqs []LookupRequest)`. Embedder calls batched where the provider supports it.

### 3.11 In-process embedding-result LRU

- **What:** Tiny LRU on the embedder wrapper to avoid re-embedding identical normalized prompts. Extend `pkg/embedding/throttled`.

### 3.12 Weaviate and Milvus vector backends

- **What:** Round out the "big four" vector DBs using scaffolding from 2.15/2.16.

### 3.13 S3 / GCS cold-tier store

- **What:** Tiered store: hot in Redis/Badger, cold in S3 with lazy promote. New `store.TieredStore` composing two sub-stores.

### 3.14 Capacity-planning calculator

- **What:** A script in `RUNBOOK.md` that takes (req/sec, prompt-len, embedding-dim, expected hit-rate) and outputs (memory, RPS budget per backend, $/month).

### 3.15 Optional `LookupOrFetch` LLM-gateway mode

- **What:** Opt-in `LookupOrFetch(ctx, req, llm Provider)` API and corresponding HTTP shim that calls the host's LLM provider on miss, then `Store()`s the result. Lookup-only path remains the primary, recommended integration.
- **Why:** Removes the boilerplate "on miss, call LLM, store result" pattern for the common case. Complements 1.7 (OpenAI-compatible reverse proxy) by extending it to user-supplied provider adapters.
- **Constitutional note:** This intentionally departs from `improvement_plan2.md §4`, which argues against a built-in gateway on the grounds that LiteLLM/Portkey/OpenRouter occupy that space. The constitutional decision: ship the convenience helper opt-in, keep the lookup-only API primary, and resist any drift toward routing or model-selection features (which remain out of scope per `mission.md`).

### 3.16 Cut 1.0

- **What:** Freeze the public API per `tech-stack.md`. Audit all opt-in feature flags and graduate or remove. Tag 1.0.0; follow the COMPATIBILITY.md semver contract from this point on.

### Phase 3 exit criteria

- Polyglot story complete (Python, JS, Java, Rust SDKs).
- Enterprise-readiness items shipped: audit log, key rotation, GDPR endpoint, mTLS, JWT/OIDC, PII redaction.
- Optional gateway mode available without compromising the lookup-only primary path.
- 1.0.0 tagged; semver contract in force.

---

## OKRs by phase

These targets are taken from `improvement_plan2.md §5` and adopted as the constitutional success metrics.

| Metric | Now (Phase 1) | Next (Phase 2) | Later (Phase 3) |
|---|---|---|---|
| GitHub stars | 1k | 3k | 10k |
| Non-Go production deployments | 5 | 25 | 100 |
| 3rd-party backend adapters merged | 1 (pgvector) | 4 | 6 |
| Median time-to-first-hit (new user) | 5 min (Python SDK) | 2 min (LangChain one-liner) | 1 min |
| False-positive rate at default threshold | 0/100 (with reranker) | 0/1000 | 0/1000 |
| `/metrics` adoption rate (% of deploys exposing it) | 80% | 95% | 95% |

---

## Decision principles when this roadmap is in tension

When two roadmap items conflict, resolve in this order:

1. **Correctness over hit-rate** (per `mission.md` principle 1).
2. **Library API stability** (per `mission.md` principle 4) — never sacrifice it for roadmap velocity.
3. **Zero-deps Quick Start** (per `mission.md` principle 2) — new mandatory dependencies need constitutional amendment, not roadmap permission.
4. **Pluggable everything** (per `mission.md` principle 3) — features that work only with one backend are deferred until the interface and the second implementation exist.
5. Roadmap sequence — if (1)–(4) are satisfied, follow the phase ordering above.

Items can be promoted across phases if they unblock disproportionate user value, but the constitutional principles always take precedence over phase scheduling.
