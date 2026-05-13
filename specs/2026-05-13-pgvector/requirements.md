# Requirements — Postgres + pgvector backend

Phase: 1
Date: 2026-05-13
Branch: spec/phase-1-pgvector

## Context

The two prior Phase-1 specs shipped the adoption surface and the
runtime/correctness items. One Phase-1 exit criterion remains open:
**"pgvector backend merged with conformance compliance"**
(`specs/roadmap.md:106`). The core-readiness requirements explicitly
deferred it to a separate spec
(`specs/2026-05-10-core-readiness/requirements.md:61`) — this is that
spec.

Per `specs/mission.md` §Audience, the platform/SRE secondary persona
already runs Postgres in production. pgvector unifies the two
Reverb extension points (`store.Store` *and* `vector.Index`) into one
backend with shared connection pooling — operators run one database,
not two — and the roadmap calls it out as **"the first
community-requested backend"** (`specs/roadmap.md:79`). New backends
must pass `pkg/store/conformance` and `pkg/vector/conformance` before
merge (`specs/tech-stack.md:38`).

## Scope

- **`internal/pgbackend`** — single canonical layer owning the Postgres
  schema, embedded migrations, and `*pgxpool.Pool` lifecycle. Both
  public adapters delegate to it. Not part of the public API; lives in
  `internal/` per `specs/tech-stack.md:25` so the layout can keep
  moving.
- **`pkg/store/postgres`** — thin public adapter that implements
  `store.Store` against `internal/pgbackend`. Passes
  `pkg/store/conformance`.
- **`pkg/vector/pgvector`** — thin public adapter that implements
  `vector.Index` against `internal/pgbackend`. Passes
  `pkg/vector/conformance`.
- **Shared `*pgxpool.Pool`** — operator-supplied pool reused across
  both adapters. The standalone binary opens one pool from
  `postgres.dsn` and hands it to a single `newBackends(cfg)` factory
  that returns the matched pair, owning migration ordering and shutdown
  in one place.
- **Additive `vector.Index.Search` signature** — current method is
  `Search(ctx, query []float32, k int, minScore float32) ([]SearchResult, error)`
  (`pkg/vector/index.go:11`). pgvector needs to pre-filter by
  namespace/model before ANN to avoid losing recall to wrong-namespace
  candidates. Add a variadic `opts ...SearchOption` parameter; existing
  `flat` / `hnsw` impls accept and ignore the opts (post-filter
  behavior in `pkg/cache/semantic/semantic.go:99` stays as defense in
  depth). Reverb is pre-1.0 (`specs/roadmap.md:321` 3.16 cuts 1.0), so
  this minor-version interface change is permitted per
  `mission.md` principle 4.
- **Embedded migrations via goose** — single migration set under
  `internal/pgbackend/migrations/`, embedded via `embed.FS`, runnable
  programmatically through an explicit `Migrate(ctx, pool) error` —
  **not** auto-run on `New()`. Constructors are pool-ping +
  prereq-check only, so importing the backend has no surprising side
  effects.
- **Standalone-binary wiring** — `cfg.Store.Backend = "postgres"` plus
  `cfg.Vector.Backend = "pgvector"` (existing field names, see
  `pkg/reverb/config.go:54` and `:70`) trigger the shared-pool path
  through a new `newBackends(cfg)` in `cmd/reverb/main.go` that
  replaces the independent `newStore` / `newVectorIndex` switch
  arms for this backend pair.
- **`--validate` extension stays readiness-only** — the current
  contract (`cmd/reverb/main.go:441`) explicitly skips embedder calls.
  For postgres mode, extend the readiness probe with: pool ping,
  `vector` extension present, pgvector version ≥ pinned floor,
  current migration version matches embedded. **No** synthetic
  semantic lookup; no embedder dependency.
- **Docker-compose integration test** — `ankane/pgvector` container,
  `make test-integration-pg` runs the gated conformance suites and the
  end-to-end cache test against it.
- **Split latency baseline** in `BENCHMARKS.md`: three buckets —
  exact-store `GetByHash`, vector-only `Search`, end-to-end cache
  lookup. Blended numbers are not useful for comparing schema/index
  changes.
- **Migration guide + CHANGELOG entry** in `COMPATIBILITY.md` and
  `CHANGELOG.md` for operators moving from memory/Redis/Badger.

## Non-goals

- **IVFFlat index type.** pgvector supports HNSW and IVFFlat; v1 ships
  HNSW only. IVFFlat can be added later behind a constructor option
  without breaking the interface.
- **Postgres-native CDC listener.** A `pkg/cdc/postgres` listener
  that consumes logical replication is a natural follow-on but doubles
  the surface area and the conformance burden — out of scope here.
- **Multi-tenant connection-pool partitioning.** Uses `pgxpool`
  defaults; tenant-aware pool partitioning is a separate concern.
- **Hot-path counter maintenance for `Stats`.** First cut uses
  cheap `COUNT(*)` queries; if hot enough to need maintained counters
  later, that's an additive change.
- **Per-namespace partial HNSW indexes.** Considered and rejected in
  favor of one HNSW index plus a namespace side-column with WHERE
  pre-filter — partial-index proliferation isn't worth the recall
  gain at expected cache sizes.
- **Replacing `lib/pq` or `database/sql` callers elsewhere.** This
  spec adopts `jackc/pgx v5` for the new backend only; no migration
  of unrelated code.

## Decisions

- **Architecture: shared internal layer with thin public adapters.**
  `internal/pgbackend` owns the schema, the migrations, and the pool
  lifecycle. `pkg/store/postgres` and `pkg/vector/pgvector` are
  ~50-line adapters that translate between the public interfaces and
  the shared layer. Honest about the shared schema; preserves the
  established `pkg/store/<backend>` and `pkg/vector/<backend>` layout
  every other backend follows.
- **Driver: `jackc/pgx` v5 via `pgxpool`.** Idiomatic modern Postgres
  driver. Added as an opt-in dependency to `internal/pgbackend` and
  the two adapter packages only — does not enter the core dependency
  graph per `mission.md` principle 2.
- **Migrations: `pressly/goose` v3, embedded via `embed.FS`.** Single
  canonical migration set in `internal/pgbackend/migrations/`. Goose's
  library API supports programmatic `embed.FS` runners with advisory
  locks (safe across concurrent processes). Explicit
  `pgbackend.Migrate(ctx, pool) error` — adapter constructors do
  **not** auto-migrate.
- **Index: HNSW only.** pgvector 0.7+. Build params (`hnsw.m`,
  `hnsw.ef_construction`) and query param (`hnsw.ef_search`) all
  honor the existing config fields at `pkg/reverb/config.go:71-73`.
  `ef_search` is set per-session before each `Search` call.
- **Vector schema includes `namespace` and `model_id` side columns.**
  Pre-filter via `WHERE namespace = $1 AND (model_id = $2 OR $2 = '')`
  before ANN. Avoids wrong-namespace candidates consuming the top-k
  budget. Documented recall/cost tradeoff in `BENCHMARKS.md`.
- **`vector.Index.Search` gains a variadic `opts ...SearchOption`
  parameter.** New options package-level constructors:
  `WithNamespace(string)`, `WithModelID(string)`. `flat` and `hnsw`
  impls accept the opts but no-op them (post-filter in
  `pkg/cache/semantic/semantic.go` continues to handle correctness
  for those backends). pgvector applies them in SQL. Conformance suite
  gains a new `SearchScopedByNamespace` test case that's
  capability-gated.
- **Lineage: normalized `entry_sources` table.**
  `entry_sources(entry_id uuid, source_id text, content_hash bytea,
  PRIMARY KEY (entry_id, source_id))` plus `CREATE INDEX ON
  entry_sources (source_id)`. `ListBySource(sourceID)` is a single
  indexed scan; `ContentHash` is preserved (the reviewer's
  correctness point — invalidation engines compare hashes, not just
  IDs).
- **Entry table preserves every `store.CacheEntry` field.** Notably
  `ResponseMeta` (jsonb), `EmbeddingMissing` (bool), and `Embedding`
  (the canonical copy lives on the `embeddings` row, with the
  `entries` table holding only the `embedding_missing` flag and a
  reference to the embedding row via FK).
- **Dimension source of truth: `embedding.dimensions` in config.**
  The migration template substitutes `vector(N)` from
  `cfg.Embedding.Dimensions` (`pkg/reverb/config.go:50`). On
  `pgbackend.New()`, the column dimension is read from
  `information_schema.columns` and compared with the config; any
  mismatch fails fast with a typed error. `Add` payloads validate
  against the resolved dimension and return
  `vector.ErrDimensionMismatch` on mismatch — same error wording the
  conformance suite asserts (`pkg/vector/conformance/conformance.go:122`).
- **Schema namespaced under `reverb` by default.** Single `WithSchema`
  option overrides for operators co-located in a shared database.
- **`Len()` semantics: process-local cached count.** The vector
  interface requires `Len() int` (`pkg/vector/index.go:22`) and
  conformance asserts exact local behavior
  (`pkg/vector/conformance/conformance.go:81`). For a shared Postgres
  index, an authoritative global count would race; instead, the
  adapter maintains a per-instance atomic counter incremented/decremented
  on `Add`/`Delete`, seeded from a one-time `SELECT COUNT(*)` at
  `New()`. Documented on the godoc as "this instance's view" rather
  than authoritative.
- **Backend stays opt-in.** Per `mission.md` principle 2, Quick Start
  still runs with `memory.New()` + `flat.New()` + `fake.New()` — no
  Postgres required. The new packages are opt-in imports.
- **CGO stays off.** pgx and pgvector both work pure-Go; the
  `CGO_ENABLED=0` build invariant from `tech-stack.md:14` holds.

### Open questions

- [ ] **pgvector version floor.** Probably 0.7.0 (HNSW + iterative
      index scans). Confirm against the `ankane/pgvector` Docker image
      pinned in CI.
- [ ] **Pool defaults.** Rely on pgx defaults (`max=4*cpu`), or set
      explicit defaults sized for typical Reverb workloads? Default
      proposal: pgx defaults, with `WithPoolConfig` for override.
- [ ] **`hnsw.iterative_scan` toggle.** pgvector 0.8+ supports
      iterative index scans to recover recall lost to WHERE filters.
      Default proposal: enable when available, document version
      sensitivity.
- [ ] **Overfetch knob.** Even with pre-filter, callers might want an
      overfetch multiplier as belt-and-suspenders. Default proposal:
      not in v1 — pre-filter alone is enough; revisit if integration
      tests reveal recall issues.

## References

- `pkg/store/store.go:58` — actual `store.Store` interface.
- `pkg/vector/index.go:11` — actual `vector.Index` interface.
- `pkg/store/store.go:9` — actual `CacheEntry` shape; the schema
  must preserve every non-internal field.
- `pkg/reverb/config.go:53-73` — `StoreConfig` and `VectorConfig`
  field names (`Backend`, `HNSWm`, `HNSWefConstruct`, `HNSWefSearch`)
  this spec must reuse.
- `pkg/cache/semantic/semantic.go:80` — call site that will pass
  `WithNamespace` / `WithModelID` after the interface change.
- `pkg/vector/conformance/conformance.go:81,112` — `Len()` and
  dimension-mismatch contracts the adapter must satisfy.
- `cmd/reverb/main.go:441` — `--validate` contract this spec
  extends but does not redefine.
- `specs/mission.md` — principles 2, 3, 4.
- `specs/tech-stack.md` — §"Pluggable interfaces", §"Dependency
  policy", §"Language and runtime".
- `specs/roadmap.md` — Phase 1 item 1.10; Phase 1 exit criteria.
- `specs/2026-05-10-core-readiness/requirements.md:61` — deferral
  note that scheduled this spec.
- [pgvector README](https://github.com/pgvector/pgvector/blob/master/README.md)
  — authoritative source for HNSW params and `hnsw.ef_search`
  session-variable semantics.
