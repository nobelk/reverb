# Semantic Cache Example

Demonstrates reverb's two-tier caching pipeline using in-process components
(no external dependencies required).

## What is demonstrated

1. **Exact-match lookup** — identical prompt text returns tier=`exact`, similarity=1.0
2. **Cache miss** — unrelated queries return no result
3. **Namespace isolation** — entries stored in one namespace are invisible to another
4. **Source lineage and invalidation** — entries linked to a source document are evicted
   when that source is invalidated (simulates a doc update)
5. **Stats** — aggregate counters for hits, misses, and invalidations

> **Note on semantic hits:** This example uses `fake.New(64)`, a deterministic
> hash-based embedder. Different strings produce uncorrelated vectors, so you
> will only see exact-match hits here. Replace the fake embedder with
> `openai.New(...)` or `ollama.New(...)` to observe true semantic (tier=`semantic`)
> hits for paraphrased queries like "password reset help".

## Run locally

```bash
# from the repo root
go run ./examples/semantic-cache
```

## Run with Docker

```bash
# from the repo root
docker compose -f examples/semantic-cache/docker-compose.yml up --build
```
