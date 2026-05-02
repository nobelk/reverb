# Reverb Production Runbook

Operational guide for running Reverb in production. For API and architecture
reference, see [`README.md`](README.md) and [`reverb-design-doc.md`](reverb-design-doc.md).

- [Deployment Modes](#deployment-modes)
- [Configuration Reference](#configuration-reference)
- [Persistence & Restart Behavior](#persistence--restart-behavior)
- [Common Failure Modes](#common-failure-modes)
- [Capacity & Performance Knobs](#capacity--performance-knobs)
- [Debugging Workflow](#debugging-workflow)
- [Rollback & Upgrade Notes](#rollback--upgrade-notes)
- [SDK Release Coordination](#sdk-release-coordination)

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
| `store.rebuild_vector_index_on_startup` | `false` | Set `true` for durable stores when semantic hit-rate dips on restart are unacceptable. See [Persistence & Restart Behavior](#persistence--restart-behavior). |
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

## Persistence & Restart Behavior

Reverb has two pieces of state with **different durability characteristics**:

1. **The store** (`pkg/store`) holds cache entries, hash indices, and lineage
   indices. `memory` is volatile; `badger` and `redis` are durable subject to
   their own persistence configuration (detailed below).
2. **The vector index** (`pkg/vector`) is **always in-memory-only** regardless
   of which store backend is configured. `hnsw` and `flat` live entirely in
   process heap. There is no on-disk representation.

These are independent. A durable store + volatile vector index is the default,
and it is the single most common source of operator surprise. This section
makes the behavior explicit.

### Durability per store backend

| Backend | Survives clean shutdown? | Survives OS crash / power loss? | Notes |
|---|---|---|---|
| `memory` | **No** | **No** | Every restart is a cold start. |
| `badger` | **Yes** (if `Close()` runs) | **Partial — tail of WAL may be lost** | Opens `badger.DefaultOptions`, which sets `SyncWrites: false`. Writes land in the value log via mmap but are not fsync'd per operation. A clean `client.Close()` flushes pending writes; a `kill -9` or power loss can drop the last few seconds of writes. Acceptable for a cache — not acceptable as a general-purpose database. |
| `redis` | **Depends on Redis config** | **Depends on Redis config** | Reverb treats Redis as a pure backing store. Durability is whatever Redis is configured to provide — see below. |

#### Badger specifics (`pkg/store/badger/badger.go:33`)

- The process holds an **exclusive directory lock** for the lifetime of the
  store. Two Reverb processes cannot share a Badger path. In Kubernetes, use a
  `StatefulSet` with one `PersistentVolume` per replica.
- `LSM` and value-log files live under the configured path. The directory must
  be writable by the container UID (`appuser` in the production image).
- Changing `embedding.dimensions` between runs is **not** caught by Badger —
  the stored entries still load, but `hnsw.Index.Add` will reject every
  mismatched vector at insert time. See [HNSW Dimension
  Mismatch](#hnsw-dimension-mismatch).

#### Redis specifics (`pkg/store/redis/redis.go`)

Reverb writes three kinds of keys (`reverb:entry:*`, `reverb:hash:*`,
`reverb:lineage:*`) and uses Lua `EVAL` scripts for atomic Put / IncrementHit.
It does **not** configure Redis persistence — that's the operator's
responsibility. For production, pick one:

- **`appendonly yes`** (AOF, recommended): every write is logged and fsync'd
  per `appendfsync` setting (`everysec` is the usual compromise). Matches
  Badger's "lose at most a few seconds" profile.
- **RDB snapshots only** (default in many images): a crash loses every write
  since the last snapshot. Explicitly unsuitable for multi-tenant
  deployments where "yesterday's cache was warm" is load-bearing.
- **Neither** (`save ""` + no AOF): cache is effectively volatile; restarting
  Redis is equivalent to `FLUSHALL`.

Reverb does **not** detect Redis persistence config. If semantic hit rate
mysteriously drops after a Redis restart, check `CONFIG GET appendonly` first.

Reverb's Lua scripts assume a **single primary**. Redis Cluster routing is
not supported — `EVAL` with multi-slot keys will fail.

### Vector index: always cold on process start

Both `flat.New` (`pkg/vector/flat/flat.go:23`) and `hnsw.New`
(`pkg/vector/hnsw/hnsw.go:63`) initialize an empty map / graph. There is no
serialization, no snapshot, no mmap. A freshly-started Reverb process **has
no vectors in its index until one of two things happens**:

1. A `Store()` call adds a new entry (and its freshly-computed embedding).
2. `WithRebuildVectorIndex(true)` scans the store and re-adds existing
   embeddings (see next section).

During this warmup window, the exact tier (Tier 1) is fully functional — it
reads from the store by prompt hash — but the semantic tier (Tier 2) returns
misses. This is a silent behavior: the process is healthy, `/v1/stats`
reports a normal hit rate on exact hits, and the degraded semantic path only
shows up as a hit-rate regression relative to steady state.

### Startup reconciliation options

By default, nothing reconciles the store and the vector index at boot. Two
modes are supported:

| Mode | Config | Behavior | When to use |
|---|---|---|---|
| **Lazy (default)** | `store.rebuild_vector_index_on_startup: false` | The index warms as new entries are stored. Semantic hits degrade for minutes to hours depending on traffic. | Low-durability caches, memory store, dev. |
| **Eager rebuild** | `store.rebuild_vector_index_on_startup: true` | `New()` scans every namespace and re-adds every non-expired entry's embedding before returning. Synchronous. Library callers can also pass `reverb.WithRebuildVectorIndex(true)`. | Durable stores (badger, redis) with semantic-hit-rate SLOs that cannot tolerate warmup latency. |

Trade-offs of eager rebuild:

- **Startup latency is O(N)** in the number of stored entries. At 10M entries
  and ~1ms per `hnsw.Add`, expect ~3 hours. Size your readiness probe budget
  accordingly or keep the eager mode off for cache sizes above ~100K.
- **Failures fail the process.** If `store.Scan` or `vectorIndex.Add`
  returns an error, `reverb.New` returns that error and the client is not
  usable. This is deliberate — a partial index would silently serve a mix of
  reconciled and cold semantic lookups.
- **Expired entries are skipped** during the scan, so eager rebuild does not
  re-index garbage the reaper is about to delete.
- **Entries with `EmbeddingMissing: true`** (embedder failures at Store time)
  are skipped — they never had a vector to begin with.
- **Dimension mismatches are surfaced here first.** If `embedding.dimensions`
  changed between runs, the first `vectorIndex.Add` fails and startup aborts.
  The lazy path would instead silently reject every new write at runtime.

### What's guaranteed across a restart

Given a clean shutdown and a durable backend (badger, or redis with AOF):

- **Exact-tier lookups** for previously-stored entries hit on first call.
- **Lineage-based invalidation** (`POST /v1/invalidate`) works — the
  `lineage:` keys are in the store.
- **Hit counters** (`HitCount`, `LastHitAt`) are preserved.
- **TTLs** are preserved; the expiry reaper picks up where it left off (the
  reaper runs every 5 minutes and tolerates any delay).

Given a clean shutdown but **lazy** reconciliation:

- Semantic-tier lookups miss until the corresponding entry is re-stored.
- Paraphrase-heavy namespaces will show a visible hit-rate dip for the
  duration of warmup.

Given an **unclean** shutdown (kill -9, OOM, power loss):

- Badger with default `SyncWrites: false`: up to ~seconds of writes may be
  lost.
- Redis with AOF `everysec`: up to ~1 second of writes may be lost.
- Redis with RDB only: writes since the last snapshot may be lost.

For cache workloads, each of these is acceptable. For workloads where cache
loss equals a cost-incident (expensive LLM calls, tight SLOs), enable
`WithSyncWrites(true)` on Badger via a future option, or pick Redis AOF
`always` — both trade write throughput for durability.

### Operator checklist

Before deploying to production:

- [ ] `store.backend` is `badger` or `redis` (not `memory`).
- [ ] For Redis: `CONFIG GET appendonly` returns `yes`.
- [ ] For Badger: the mounted directory is on a volume that survives pod
  restarts (not an `emptyDir`).
- [ ] `store.rebuild_vector_index_on_startup` is set deliberately — either
  `true` with a known cache size, or `false` with an understanding that
  semantic hit rate will dip on restart.
- [ ] Your readiness probe tolerates the rebuild duration if eager mode is on.
- [ ] Graceful shutdown is wired (the standalone binary already handles
  SIGINT / SIGTERM via `signal.NotifyContext`).

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

After any restart the vector index is empty — regardless of store backend.
See [Persistence & Restart Behavior](#persistence--restart-behavior) for the
full story and the `store.rebuild_vector_index_on_startup` knob. Expected
semantic hit rate during lazy warm-up: near zero. Compare
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
  serving. By default the vector index rebuilds lazily (existing Redis
  entries are not auto-indexed at startup) — set
  `store.rebuild_vector_index_on_startup: true` to reindex eagerly. See
  [Persistence & Restart Behavior](#persistence--restart-behavior).
- **Badger store:** cycle replicas one at a time — the directory lock
  prohibits parallel startup against the same volume. Same vector-index
  rebuild trade-off as Redis.

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

---

## SDK Release Coordination

The Python (`sdk/python`, published as `reverb` on PyPI) and TypeScript
(`sdk/js`, published as `@reverb/client` on npm) clients are generated from
[`openapi/v1.yaml`](openapi/v1.yaml) — that file is the single source of
truth for the HTTP wire surface.

> **Sibling-repo migration.** The SDKs currently live in this repo under
> `sdk/python` and `sdk/js`. Per `specs/tech-stack.md` §"Repository
> composition" the main repo will eventually be Go-only and these will move
> to `nobelk/reverb-python` and `nobelk/reverb-js`. The release-coordination
> rules below apply equally before and after that split — only the file
> paths in step 2 change.

### Release-order invariant

When a release changes `openapi/v1.yaml` in any way that affects the public
HTTP surface, the SDKs must be regenerated and tagged **before** the
main-repo release goes out:

1. **Land the OpenAPI change** on the main-repo PR. The
   `openapi_drift_test` CI job must be green — drift between the spec and
   the Go handlers is a release blocker.
2. **Regenerate SDKs** from the merged spec:
   ```bash
   make sdk-regen-python    # rebuilds sdk/python wire client
   make sdk-regen-js        # rebuilds sdk/js wire client
   ```
   Inspect the diff. Schema-additive changes (a new optional field) are
   safe; renames and removals are breaking and require a major bump.
3. **Bump SDK versions** to a matching minor (`reverb` and `@reverb/client`
   move together). Backwards-compatible changes get a minor bump; breaking
   changes get a major bump and force a `/v2/` path on the server side per
   the additive-evolution rule in `openapi/v1.yaml`.
4. **Cut SDK releases first.** Tag and publish the SDKs to PyPI and npm
   with the new version. The `sdk-python` and `sdk-js` CI jobs must be
   green — both run their smoke suites against a `cmd/reverb` built from
   the merge candidate.
5. **Cut the main-repo release.** Only after both SDKs are live on their
   registries. This ordering ensures that a user who reads the new
   release notes and runs `pip install -U reverb` finds the matching
   version — never a "package not found" or a stale 0.N-1 client.

### What "matching" means

| Main-repo release | `reverb` (PyPI) | `@reverb/client` (npm) |
|---|---|---|
| `v0.5.0` | `0.5.0` | `0.5.0` |
| `v0.5.1` (patch — server only, no API change) | `0.5.0` (unchanged) | `0.5.0` (unchanged) |
| `v0.6.0` (minor — additive `/v1/` change) | `0.6.0` | `0.6.0` |
| `v1.0.0` (major — `/v2/` introduced) | `1.0.0` | `1.0.0` |

Patch releases that only touch Go server internals (a Redis store fix,
HNSW tuning) do **not** require an SDK release. The Python/JS releases
move only when the OpenAPI surface moves.

### When the SDK CI is red

If `sdk-python` or `sdk-js` fails on the main-repo release-candidate
branch:

- **Do not** publish the main-repo release. The SDKs reference each main
  release as their tested-against version; shipping a main release that
  the SDK CI flagged as broken means an `npm install` user gets an SDK
  that fails against the new server.
- Diagnose first. The smoke jobs run lookup → store → lookup → invalidate;
  a failure here usually means a request body shape changed in the Go
  handler without a matching `openapi/v1.yaml` update — which the
  `openapi_drift_test` should already have caught. If the drift test was
  green but the SDK smoke is red, you've found a gap in the drift test
  worth fixing before continuing.

### Pre-flight checklist (release captain)

Before tagging a main-repo release that bumps the OpenAPI minor:

- [ ] `openapi_drift_test` green on the merge candidate.
- [ ] `make sdk-regen-python` produces no diff (or the diff is reviewed
      and matches the OpenAPI changes intentionally).
- [ ] `make sdk-regen-js` produces no diff (same).
- [ ] `sdk-python` workflow green.
- [ ] `sdk-js` workflow green.
- [ ] PyPI and npm tokens for the release pipeline are still valid
      (rotated annually).
- [ ] CHANGELOG entries for `sdk/python/CHANGELOG.md` and
      `sdk/js/CHANGELOG.md` cite the matching main-repo version.
