# Reverb Production Runbook

Operational guide for running Reverb in production. For API and architecture
reference, see [`README.md`](README.md) and [`reverb-design-doc.md`](reverb-design-doc.md).

- [Deployment Modes](#deployment-modes)
- [Configuration Reference](#configuration-reference)
- [Common Failure Modes](#common-failure-modes)
- [Capacity & Performance Knobs](#capacity--performance-knobs)
- [Debugging Workflow](#debugging-workflow)
- [Rollback & Upgrade Notes](#rollback--upgrade-notes)

---

## Deployment Modes

Reverb supports three operational shapes. Pick based on whether callers are
in-process, on the same network, or across language boundaries.

### 1. Embedded Library

Import `github.com/nobelk/reverb/pkg/reverb` directly. No server process, no
network hop. Cache state lives inside the calling process unless a shared
backend (Redis, Badger) is configured.

- **Use when:** the caller is a Go service that owns its cache tier.
- **Backends:** `memory` + `flat` for dev; `redis` + `hnsw` for shared state.
- **CDC:** polling or custom listeners only — the webhook/NATS listeners can
  also be wired via `reverb.WithCDCListener`.
- **Scaling:** horizontal scaling requires a shared store (Redis/Badger). With
  `memory`, each replica holds an independent cache.

### 2. Standalone HTTP Server

Run `cmd/reverb` and talk to it via the REST endpoints documented in the
README. This is the default container entrypoint.

- **Ports:** `8080` (HTTP). Configurable via `--http-addr` or
  `server.http_addr`.
- **Liveness:** `GET /healthz` returns `200 OK` when the process is up.
- **Use when:** you have non-Go clients or want to decouple cache lifecycle
  from application rollouts.

### 3. Standalone HTTP + gRPC Server

Set `server.grpc_addr` (e.g. `:9090`) to enable the gRPC transport in parallel
with HTTP. Both transports share a single `reverb.Client` instance and
therefore a single cache state.

- **Ports:** `8080` (HTTP), `9090` (gRPC).
- **Auth:** the same `AuthConfig` applies to both transports — bearer token on
  HTTP, API-key metadata on gRPC.
- **Wire contract:** the gRPC surface is defined by
  `pkg/server/proto/reverb.proto` and the server registers against the
  `protoc`-generated stubs in `pkg/server/proto/` (`reverb.pb.go`,
  `reverb_grpc.pb.go`). Clients in any language can generate their own stubs
  from the same `.proto` and interoperate over standard Protocol Buffers.

### 4. Container / Docker

The production `Dockerfile` produces a non-root Alpine image with a built-in
`HEALTHCHECK` against `/healthz`. It exposes `8080`, `9090`, `9091`, and
`9100`.

```bash
docker build -t reverb:latest .
docker run -d --name reverb \
  -p 8080:8080 -p 9090:9090 \
  -v $(pwd)/reverb.yaml:/etc/reverb/config.yaml \
  -e REVERB_EMBEDDING_API_KEY=sk-... \
  reverb:latest --config /etc/reverb/config.yaml
```

`9091` is reserved for the webhook CDC listener; `9100` is reserved for the
future Prometheus metrics server (see [Known Gap](#known-gap-metrics-not-wired)).

---

## Configuration Reference

The authoritative schema lives in `pkg/reverb/config.go` and the full YAML
example is in the [README §Operator Configuration](README.md#operator-configuration).
This section highlights **production-critical settings only**.

| Setting | Default | Production guidance |
|---|---|---|
| `default_ttl` | `24h` | Set deliberately. Longer TTLs reduce LLM cost but amplify stale-knowledge risk when CDC is disabled. |
| `similarity_threshold` | `0.95` | See [Performance Knobs](#capacity--performance-knobs). Do not drop below `0.90` without paraphrase evaluation data. |
| `semantic_top_k` | `5` | Controls vector index fan-out; raising increases latency linearly. |
| `scope_by_model` | `true` | **Keep `true`** in multi-model deployments. Turning off allows a `gpt-4o` entry to serve a `claude-opus-4-6` query. |
| `store.backend` | `memory` | `memory` loses all state on restart. Use `redis` or `badger` in production. |
| `vector.backend` | `flat` | `flat` is O(n) and scans every entry — switch to `hnsw` above ~50K entries. |
| `auth.enabled` | `false` | **Enable** on any internet-reachable deployment. API keys are hashed at boot; rotation requires a restart. |
| `otel.enabled` | `false` | Enable to emit OTLP HTTP traces to your collector. |

### Secret Handling

Never commit `api_key` or `redis_password` to the YAML file. Use the env-var
overrides:

- `REVERB_EMBEDDING_API_KEY` → `embedding.api_key`
- `REVERB_REDIS_PASSWORD` → `store.redis_password`
- `REVERB_AUTH_API_KEY` → auto-creates a `default` tenant (single-key deployments only)

### Auth Invariants

`cfg.Validate()` (see `pkg/reverb/config.go:128`) enforces these at boot:

- If `auth.enabled`, at least one tenant must be configured.
- Every tenant must have a non-empty `id` and at least one API key.
- Tenant IDs and API keys must be globally unique (an API key cannot be shared
  across tenants).

A misconfiguration here causes the process to exit with code 1 at startup, not
a runtime surprise — fail-fast is intentional.

---

## Common Failure Modes

### Embedding Provider Unreachable

**Symptoms:** `reverb_embedding_errors_total` climbs; `/v1/store` requests
return 5xx; semantic-tier lookups return miss (the exact tier still works).

**Diagnose:**
```bash
# OpenAI: check key and quota
curl -H "Authorization: Bearer $REVERB_EMBEDDING_API_KEY" \
  https://api.openai.com/v1/models | jq .data[0]

# Ollama: check local daemon
curl http://localhost:11434/api/tags
```

**Resolve:** rotate the key, lift the rate limit, or fall back to `ollama` for
local inference. The exact-match tier is unaffected — do **not** restart unless
the embedder is permanently misconfigured.

### Redis Unavailable

**Symptoms:** `/v1/lookup` and `/v1/store` return 5xx; `pkg/store/redis` errors
in logs (wrapped `goredis.Client` connection refused / context deadline).

**Diagnose:**
```bash
redis-cli -h "$REDIS_HOST" -a "$REVERB_REDIS_PASSWORD" ping
redis-cli -h "$REDIS_HOST" --scan --pattern 'reverb:*' | head
```

**Resolve:** Reverb does not auto-failover between stores. Restore Redis or
swap to `store.backend: memory` (cache is sacrificed, not durable) and
restart. The Lua scripts in `pkg/store/redis/redis.go` assume a single
primary — **do not** point Reverb at a Redis Cluster without first verifying
script routing.

### BadgerDB Lock / Path Errors

**Symptoms:** Process fails at startup with `Cannot acquire directory lock` or
permission errors; `cmd/reverb` exits with "failed to create store".

**Resolve:** Only one process may hold a Badger directory at a time. In Kubernetes,
use a `StatefulSet` with a `PersistentVolume` per replica, or switch to `redis`.
Ensure the `appuser` UID inside the container can write to `badger_path` — the
image runs as non-root.

### HNSW Dimension Mismatch

**Symptoms:** Every `/v1/store` after an upgrade returns an error containing
`vector dimension`; `reverb_stores_total` goes to zero but `reverb_lookups_total`
still serves stale semantic hits.

**Cause:** `pkg/vector/hnsw/hnsw.go` validates that each vector matches the index
dimensionality. Changing `embedding.model` or `embedding.dimensions` without
rebuilding the index produces this state.

**Resolve:** either revert the embedding change, or wipe and rebuild. With
Redis: `redis-cli --scan --pattern 'reverb:*' | xargs redis-cli DEL`. With
Badger: delete `badger_path` and restart. Both actions are destructive — the
cache will cold-start.

### Auth Misconfiguration

**Symptoms:** Process refuses to start with one of the
validation errors from `config.go:128`: duplicate tenant ID, duplicate API key,
empty `api_keys`.

**Resolve:** Correct the YAML. There is no hot reload — apply changes with a
rolling restart.

### Webhook CDC Port Conflict

**Symptoms:** `cdc.enabled: true`, `mode: webhook`, and `failed to create CDC
listener` at boot or webhook listener never becomes reachable.

**Resolve:** Default `webhook_addr` is `:9091`. Change it if 9091 is already
occupied (some service meshes claim it). Verify the upstream CMS is pointing
at the configured `webhook_path`.

### Low Hit Rate

Not strictly a failure, but the most common alert. See
[Debugging Workflow §Hit-rate investigation](#hit-rate-investigation).

### Known Gap: Metrics Not Wired

`metrics.enabled` and `metrics.addr` parse cleanly, but the standalone binary
does **not** start a Prometheus HTTP endpoint. The port `9100` exposed in the
Dockerfile is a placeholder. Until this is wired:

- Use OTel tracing (`otel.enabled: true`) for per-request visibility.
- Use `GET /v1/stats` for coarse hit/miss counters.
- Track progress on the metrics server in the README footnote.

---

## Capacity & Performance Knobs

### Similarity Threshold

The single most impactful tuning knob.

| Threshold | Behavior | Failure mode |
|---|---|---|
| `0.99` | Only near-identical paraphrases match. | Miss rate high; cache underutilized. |
| `0.95` (default) | Balanced; recommended starting point. | — |
| `0.90` | Aggressive reuse; picks up looser paraphrases. | **False positive hits** — unrelated queries collapse. |
| `<0.85` | Do not use in production without evaluation. | Cache becomes a hallucination amplifier. |

Measure with `benchmark/eval_falsepositive_test.go` and
`benchmark/eval_paraphrase_test.go` before lowering. Override at runtime via
`REVERB_SIMILARITY_THRESHOLD`.

### Vector Index Choice

| Backend | Capacity | Lookup cost | When to use |
|---|---|---|---|
| `flat` | ≤ ~50K entries | O(n) cosine scan | Dev, low-traffic, or small KB. |
| `hnsw` | ≤ ~10M entries | O(log n) ANN | Anything beyond a toy cache. |

Switching requires a cold cache — the on-disk representations differ.

### HNSW Parameters

Tunable in `vector:` block; see `pkg/vector/hnsw/hnsw.go:15`.

| Param | Default | Effect |
|---|---|---|
| `hnsw_m` | 16 | Max connections per layer. Higher → better recall, more memory, slower insert. |
| `hnsw_ef_construction` | 200 | Candidate list during insert. Higher → better graph quality, slower insert. One-time cost. |
| `hnsw_ef_search` | 50 (default `100` in code) | Candidate list during query. **Primary latency/recall knob at runtime.** |

Start with defaults. Raise `ef_search` if recall is too low; lower it if p99
lookup latency exceeds budget.

### Semantic Top-K

`semantic_top_k` bounds how many vector-index candidates are fetched per
lookup. Each candidate costs one scored comparison. Leaving at `5` is fine
until a namespace has systematically low hit rates on long-tail paraphrases.

### TTL

Short TTLs trade cache efficiency for freshness. With CDC enabled, long TTLs
are safe because source changes trigger invalidation. **Without CDC**, cap at
`24h` as a safety net against stale responses.

### Scaling Out

- **Memory store:** cannot scale horizontally — each replica is isolated.
- **Redis store:** shared state, horizontal replicas OK. Contention is on the
  `putScript` Lua eval; profile Redis CPU before adding replicas beyond ~10.
- **Badger store:** single-writer only — do not share a volume.

The vector index is **per-process**. Replicas running against shared Redis
still maintain independent HNSW graphs and will warm up separately.

---

## Debugging Workflow

Logs are JSON via `slog` (see `cmd/reverb/main.go:45`). Always start with logs.

### Step 1: Is the process healthy?

```bash
curl -sf http://reverb:8080/healthz && echo OK
```

### Step 2: What does the cache think it is doing?

```bash
curl -s http://reverb:8080/v1/stats | jq
```

Returns hit/miss counters and namespace enumeration. Compare against your
expected traffic volume.

### Step 3: Correlate with traces

If `otel.enabled: true`, each HTTP request emits a span via
`otelhttp.NewHandler` (see `pkg/server/http.go:52`). The Redis store also emits
spans for `Put`/`Get`/`DeleteByIDs`. Trace a cold lookup end-to-end:

1. `reverb-http POST /v1/lookup`
2. → exact-tier store lookup
3. → embedder call (semantic tier, on miss)
4. → vector search
5. → semantic-tier store fetch

A missing span narrows the suspect component immediately.

### Hit-rate investigation

If hit rate is below expectation:

1. `GET /v1/stats` — total lookups vs. hits per namespace.
2. Sample a suspected duplicate pair with `/v1/lookup`:
   ```bash
   # Call with the original prompt, then a paraphrase.
   # If both miss and `tier: none`, the embedder or threshold is the issue.
   ```
3. Check `similarity_threshold` — paraphrases scoring `0.93` never hit a `0.95`
   threshold regardless of embedder quality.
4. Check `scope_by_model` — a query for `gpt-4o` will not match an entry stored
   for `gpt-4o-mini` when this is `true`.
5. Check CDC activity — aggressive `/v1/invalidate` webhooks can wipe the cache
   faster than it warms.

### Cold-start diagnosis

After a restart with an in-memory or flat backend, the vector index is
empty. Expected hit rate during warm-up: near zero. Compare
`reverb_entries_total` against historical steady-state.

### Staging reproduction

The `test/integration/` suite runs against a containerized server
(`make test-integration`). Use it to reproduce customer bug reports before
touching production config.

---

## Rollback & Upgrade Notes

### Version Pinning

Always deploy a specific image tag (`reverb:v0.x.y`), never `latest`. The
Dockerfile uses Go 1.25 — confirm your base image supports the target module
version before upgrading the Go toolchain.

### State Compatibility Matrix

Reverb does not encode a schema version in stored entries. These changes
**require wiping the cache** (destructive rollout):

- Changing `embedding.model` or `embedding.dimensions`.
- Switching `vector.backend` between `flat` and `hnsw`.
- Switching `store.backend` between backends.
- Any Reverb release that modifies the on-disk JSON shape of cache entries
  (check release notes).

These are **non-destructive** and can be rolled forward/back in place:

- Adjusting `similarity_threshold`, `semantic_top_k`, `default_ttl`.
- Tuning HNSW `ef_search` (construction parameters affect new inserts only).
- Toggling `auth.enabled` and adding/rotating API keys.
- Toggling `otel.enabled`, changing endpoints.
- Enabling CDC on an existing deployment.

### Rolling Restarts

- **Memory store:** every restart is a cold start. Accept a temporary hit-rate
  drop, or drain traffic before cycling.
- **Redis store:** restarts are hot; new process reconnects and resumes
  serving. HNSW graph is rebuilt lazily as new entries are inserted — existing
  Redis entries are not auto-indexed at startup. Expect a degraded semantic
  hit rate immediately post-restart until the index rewarms. For Redis-backed
  deployments this is the most common operator surprise.
- **Badger store:** cycle replicas one at a time — the directory lock
  prohibits parallel startup against the same volume.

### Rollback

Keep the previous image tag deployable. Rollback is a redeploy:

```bash
docker run ... reverb:v0.x.(y-1) --config /etc/reverb/config.yaml
```

Before rolling back, check the [State Compatibility
Matrix](#state-compatibility-matrix). If the rollback crosses a
wipe-required boundary, plan the cache flush in the same change window:

```bash
# Redis
redis-cli --scan --pattern 'reverb:*' | xargs -r redis-cli DEL

# Badger — stop the process first
rm -rf /var/lib/reverb/badger
```

### Secret Rotation

API keys and Redis passwords are read **at boot only**. To rotate:

1. Update the secret source (env var, vault injector, config YAML).
2. Rolling restart the replicas.
3. Revoke the old credential upstream.

There is no `/v1/reload` endpoint — this is deliberate; auth is pre-hashed at
startup (see `pkg/auth/auth.go:34`) for constant-time comparison.

### Upgrade Smoke Test

After any upgrade, validate with:

```bash
curl -sf http://reverb:8080/healthz
curl -s http://reverb:8080/v1/stats | jq '.hit_rate'
# issue a store + lookup round-trip against a canary namespace
```

If `hit_rate` remains at pre-upgrade levels after five minutes of steady
traffic, the upgrade is clean.
