# Stale Knowledge Prevention Example

Demonstrates reverb's primary value proposition: **automatic cache invalidation
when source documents change**, preventing a RAG chatbot from serving outdated
answers.

## Why this matters

A plain TTL-based cache has no concept of *why* a response was generated. When a
knowledge base document is updated, the cache continues serving stale answers
until the TTL expires — which could be hours or days. Reverb tracks source
lineage and evicts automatically when sources change. Stale exposure is bounded
by webhook delivery and CDC processing latency (~500ms), instead of TTL duration.

## What is demonstrated

1. **Cold cache miss** — first lookup returns no result
2. **Exact-match hit** — identical prompt served from cache (saves an LLM call)
3. **Webhook-driven invalidation** — pricing document updates, webhook fires,
   reverb evicts all cached responses derived from the old document
4. **Fresh answer after invalidation** — re-stored response with new pricing is
   served correctly
5. **Same-hash idempotency** — a webhook with an unchanged content hash does
   *not* invalidate (the core correctness property of lineage-aware caching)
6. **Cache statistics** — hit counts, miss counts, invalidation counts

## How this differs from the other examples

| Example | Store | Invalidation | Focus |
|---------|-------|-------------|-------|
| `basic` | In-memory | Manual `Invalidate()` | Core API walkthrough |
| `semantic-cache` | In-memory | Manual `Invalidate()` | Two-tier caching, namespaces |
| **`stale-knowledge`** | **Redis** | **Webhook CDC** | **Production-realistic invalidation** |

Start with `basic` to learn the API, then `semantic-cache` for the full feature
set, then this example to see how reverb works in a production deployment with
persistent storage and event-driven invalidation.

> **Note on semantic matching:** This example uses `fake.New(64)`, a
> deterministic hash-based embedder. Only identical prompts produce exact-match
> hits. The focus here is source-aware invalidation, not semantic reuse quality.
> The `flat.New()` vector index is in-process memory and does not survive
> restarts — exact-match lookups (backed by Redis) do persist, but semantic
> search state would be lost on restart.

## Run locally

Requires a running Redis instance:

```bash
# Start Redis
docker run -d --name reverb-redis -p 6379:6379 redis:7-alpine

# Run the example (from the repo root)
go run ./examples/stale-knowledge

# Cleanup
docker rm -f reverb-redis
```

## Run with Docker Compose

No host dependencies required:

```bash
# From the repo root
docker compose -f examples/stale-knowledge/docker-compose.yml up --build
```

## Expected output

```
=== Reverb Stale Knowledge Prevention Example ===

--- Step 1: Cold cache lookup ---
  prompt:  "What are your pricing plans?"
  result:  MISS (no cached entry)

--- Step 2: Store response from LLM (linked to doc:pricing) ---
  stored entry_id=...  source=doc:pricing

--- Step 3: Same question again → cache HIT ---
  prompt:  "What are your pricing plans?"
  result:  HIT  tier=exact     similarity=1.0000
  response: "Starter $9/mo, Pro $29/mo, Enterprise custom."

  [saved ~$0.03 — avoided redundant LLM call]

--- Step 4: Pricing document updated! Firing webhook... ---
  POST http://localhost:9091/hooks/source-changed → 200
  waiting for invalidation...

--- Step 5: Same question after source change → MISS ---
  prompt:  "What are your pricing plans?"
  result:  MISS (no cached entry)

  [stale answer evicted — user will NOT see old pricing]

--- Step 6: Re-store with updated pricing ---
  stored entry_id=...  source=doc:pricing

--- Step 7: Final lookup → HIT with correct new pricing ---
  prompt:  "What are your pricing plans?"
  result:  HIT  tier=exact     similarity=1.0000
  response: "Starter $19/mo, Growth $49/mo, Enterprise custom."

--- Step 8: Same content hash webhook → no invalidation ---
  POST same content_hash → 200
  result:  HIT  tier=exact     similarity=1.0000
  response: "Starter $19/mo, Growth $49/mo, Enterprise custom."
  [correct: same content hash does NOT trigger invalidation]

--- Stats ---
  exact hits:       ...
  misses:           ...
  invalidations:    1
  hit rate:         ...%

=== Without reverb, Steps 5-7 would have returned the OLD pricing. ===
=== Reverb's source lineage tracking prevented serving stale data.  ===
```
