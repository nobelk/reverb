# Plan — Postgres + pgvector backend

Phase: 1 · Date: 2026-05-13

## 1. Interface evolution: `vector.Index.Search` opts

Goal: extend the vector interface so backends can pre-filter by
namespace/model before ANN, without breaking existing call sites
beyond a one-line update.

- [ ] Add `pkg/vector/options.go`:
      - `type SearchOption func(*SearchParams)`
      - `type SearchParams struct { Namespace, ModelID string }`
      - `WithNamespace(string) SearchOption`,
        `WithModelID(string) SearchOption`.
- [ ] Change `vector.Index.Search` signature in `pkg/vector/index.go`
      to `Search(ctx context.Context, query []float32, k int,
      minScore float32, opts ...SearchOption) ([]SearchResult, error)`.
- [ ] Update `pkg/vector/flat` and `pkg/vector/hnsw` to accept the
      opts and ignore them (the existing post-filter in
      `pkg/cache/semantic/semantic.go:99` still handles correctness
      for these backends).
- [ ] Update `pkg/cache/semantic/semantic.go:80` to pass
      `vector.WithNamespace(namespace)` and (when scoping is enabled)
      `vector.WithModelID(modelID)`. Keep the existing post-filter
      loop as defense in depth — capability-aware backends just make
      it a no-op early.
- [ ] Extend `pkg/vector/conformance/conformance.go` with a new test
      `SearchScopedByNamespace` that is **capability-gated**: passes
      `WithNamespace("ns-a")` and seeds vectors in `ns-a` + `ns-b`;
      asserts only `ns-a` results come back **for backends that
      declare scoping capability** (`Index` impls expose a method
      `CapabilityScopedSearch() bool` or an interface assertion). For
      backends that don't, the test asserts post-filter handles the
      same outcome via `semantic` — so flat/hnsw stay correct.
- [ ] `CHANGELOG.md` "Changed" entry: `vector.Index.Search` signature
      gained a variadic `opts` parameter (pre-1.0 minor-version
      change; no caller-side migration beyond passing the new opts
      when desired).

## 2. `internal/pgbackend` — shared schema, migrations, pool

Goal: one canonical Postgres layer owning the schema, migrations, and
pool lifecycle. Both public adapters delegate here.

- [ ] Package skeleton: `internal/pgbackend/{pgbackend.go,
      entries.go, embeddings.go, lineage.go, migrate.go,
      migrations/, doc.go}`.
- [ ] `migrations/0001_init.sql` — full schema in one migration (the
      schema is shipped together; splitting buys nothing and creates
      partial-state edge cases):
      ```
      CREATE SCHEMA IF NOT EXISTS reverb;
      CREATE EXTENSION IF NOT EXISTS vector;

      CREATE TABLE reverb.entries (
        id                uuid PRIMARY KEY,                -- UUIDv7
        namespace         text        NOT NULL,
        prompt_hash       bytea       NOT NULL,            -- 32 bytes
        prompt_text       text        NOT NULL,
        response_text     text        NOT NULL,
        response_meta     jsonb       NOT NULL DEFAULT '{}'::jsonb,
        chunks            jsonb,                            -- nullable; streamed form
        model_id          text        NOT NULL,
        created_at        timestamptz NOT NULL,
        expires_at        timestamptz,
        hit_count         bigint      NOT NULL DEFAULT 0,
        last_hit_at       timestamptz,
        embedding_missing bool        NOT NULL DEFAULT false
      );
      CREATE UNIQUE INDEX entries_ns_prompt_hash_idx
        ON reverb.entries (namespace, prompt_hash);
      CREATE INDEX entries_expires_at_idx
        ON reverb.entries (expires_at)
        WHERE expires_at IS NOT NULL;

      CREATE TABLE reverb.embeddings (
        entry_id   uuid       PRIMARY KEY
                              REFERENCES reverb.entries(id)
                              ON DELETE CASCADE,
        namespace  text       NOT NULL,
        model_id   text       NOT NULL,
        embedding  vector({{.Dim}}) NOT NULL
      );
      CREATE INDEX embeddings_hnsw_idx
        ON reverb.embeddings
        USING hnsw (embedding vector_cosine_ops)
        WITH (m = {{.HNSWM}}, ef_construction = {{.HNSWEFC}});
      CREATE INDEX embeddings_ns_model_idx
        ON reverb.embeddings (namespace, model_id);

      CREATE TABLE reverb.entry_sources (
        entry_id     uuid  REFERENCES reverb.entries(id)
                           ON DELETE CASCADE,
        source_id    text  NOT NULL,
        content_hash bytea NOT NULL,
        PRIMARY KEY (entry_id, source_id)
      );
      CREATE INDEX entry_sources_source_id_idx
        ON reverb.entry_sources (source_id);
      ```
- [ ] Render the migration template at `Migrate` time from
      `MigrateConfig{Dim, HNSWM, HNSWEFC}` (avoids round-tripping
      runtime values into SQL string literals).
- [ ] Wire `pressly/goose` v3 as the runner. Public entry point
      `pgbackend.Migrate(ctx context.Context, pool *pgxpool.Pool,
      cfg MigrateConfig) error`. Uses goose advisory locks so
      concurrent processes serialize safely.
- [ ] `pgbackend.New(ctx, pool, opts ...Option) (*Backend, error)`:
      - Pings the pool.
      - Verifies the `vector` extension is installed and version
        ≥ pinned floor.
      - Reads dimension from `information_schema.columns` for
        `reverb.embeddings.embedding`; fails fast if it disagrees
        with `opts.ExpectedDim`.
      - Seeds an `atomic.Int64` from `SELECT COUNT(*) FROM
        reverb.embeddings` for the `Len()` cache.
      - Does **not** run migrations. Caller invokes
        `pgbackend.Migrate(...)` explicitly.
- [ ] Implement query helpers on `*Backend`:
      - `PutEntry`, `GetEntryByID`, `GetEntryByHash`,
        `DeleteEntry`, `DeleteEntries`, `ListIDsBySource`,
        `IncrementHit`, `ScanNamespace`, `Stats`.
      - `UpsertEmbedding`, `SearchEmbeddings(ns, model, vec, k,
        minScore)`, `DeleteEmbedding`, `EmbeddingCount`.
      - All take `context.Context`; all use `pgxpool.Pool.Query` /
        `QueryRow` / `Exec` / `Begin`.
- [ ] Internal-only — godoc on every exported symbol explains the
      adapter contract but the package is not in the public surface.

## 3. `pkg/store/postgres` — thin store adapter

Goal: implement `store.Store` by delegating to `internal/pgbackend`;
pass `pkg/store/conformance` against a live container.

- [ ] Package skeleton: `pkg/store/postgres/{postgres.go,
      postgres_test.go, conformance_test.go, doc.go}`.
- [ ] Constructors:
      - `New(ctx context.Context, pool *pgxpool.Pool, opts ...Option) (*Store, error)`
        — caller-owned pool; `Close()` does not close it.
      - `Open(ctx, dsn string, opts ...Option) (*Store, error)` —
        opens an internal pool that `Close()` owns.
- [ ] Implement every method on `pkg/store/store.go:58`:
      `Get`, `GetByHash`, `Put`, `Delete`, `DeleteBatch`,
      `ListBySource`, `IncrementHit`, `Scan`, `Stats`, `Close`.
      All delegate to `*pgbackend.Backend` helpers; the adapter is
      ~50 LOC of translation.
- [ ] `Put` writes the entry row and the `entry_sources` rows in
      one transaction. `SourceHashes[i].ContentHash` lands in
      `entry_sources.content_hash`.
- [ ] `Delete` / `DeleteBatch` cascade to `entry_sources` and
      `embeddings` via FK ON DELETE CASCADE.
- [ ] `Scan` uses keyset pagination on `(namespace, created_at, id)`,
      not `OFFSET` — large stores would otherwise degenerate.
- [ ] Options: `WithSchema("reverb")`, `WithLogger(slog.Logger)`.
      **No** `WithAutoMigrate` — operators call `pgbackend.Migrate`
      directly.
- [ ] Conformance: `conformance_test.go` runs the shared suite,
      gated by `REVERB_PG_DSN`. Skipped (not failed) when unset so
      the unit-test run still works without Docker.
- [ ] godoc on every exported symbol; `doc.go` cites the
      conformance contract and the shared-schema relationship with
      `pkg/vector/pgvector`.

## 4. `pkg/vector/pgvector` — thin vector adapter

Goal: implement `vector.Index` (post §1 signature change) by
delegating to `internal/pgbackend`; pass `pkg/vector/conformance`;
apply namespace/model pre-filter via the new opts.

- [ ] Package skeleton: `pkg/vector/pgvector/{pgvector.go,
      pgvector_test.go, conformance_test.go, doc.go}`.
- [ ] Constructors mirror `pkg/store/postgres`: `New` (pool) and
      `Open` (dsn).
- [ ] Implement every method on `pkg/vector/index.go:11`:
      `Add`, `Search`, `Delete`, `Len`.
- [ ] `Add(ctx, id, vector)` calls `pgbackend.UpsertEmbedding`
      against the row matching `entries.id = id`. **Requires** the
      matching entry row to exist (FK). The adapter looks up
      `namespace` and `model_id` from the entry row in the same
      transaction; if absent, returns `vector.ErrEntryMissing` (new
      typed error, documented on the godoc).
- [ ] `Search(ctx, query, k, minScore, opts...)`:
      - Resolves `SearchParams` from opts.
      - Sets `SET LOCAL hnsw.ef_search = $1` from
        `WithHNSWEFSearch` constructor option (default from
        `cfg.Vector.HNSWefSearch`).
      - Issues `SELECT entry_id, 1 - (embedding <=> $1) AS score
        FROM reverb.embeddings
        WHERE namespace = $2
          AND ($3 = '' OR model_id = $3)
          AND 1 - (embedding <=> $1) >= $4
        ORDER BY embedding <=> $1
        LIMIT $5`.
- [ ] `Delete(ctx, id)` issues `DELETE FROM reverb.embeddings
      WHERE entry_id = $1`. Decrements the `Len()` cache atomically.
- [ ] `Len() int` returns the per-instance cached count seeded at
      `New` and maintained atomically on `Add`/`Delete`. godoc
      explicitly: "This instance's view of the index. Other processes
      sharing the schema may have a different count."
- [ ] Declare scoping capability: implement the marker
      `CapabilityScopedSearch() bool` returning `true`. Conformance
      test in §1 keys off this.
- [ ] Options: `WithSchema("reverb")`,
      `WithHNSWEFSearch(int)`, `WithLogger`. No `WithAutoMigrate`.
- [ ] Conformance: standard `pkg/vector/conformance` suite plus the
      new `SearchScopedByNamespace` case. Both run under
      `REVERB_PG_DSN`.
- [ ] godoc on every exported symbol; `doc.go` cites the shared
      schema and the FK contract with `pkg/store/postgres`.

## 5. Standalone-binary wiring

Goal: `cfg.Store.Backend = "postgres"` + `cfg.Vector.Backend =
"pgvector"` boot the binary against a real Postgres + pgvector,
sharing one pool with one place owning shutdown order.

- [ ] Extend `pkg/reverb/config.go`:
      - `StoreConfig`: add `PostgresDSN string` and
        `PostgresSchema string` fields.
      - `VectorConfig`: no new fields — DSN is read from
        `StoreConfig.PostgresDSN` when `Vector.Backend == "pgvector"`,
        so a single DSN feeds both. Config validation errors fast if
        `Vector.Backend == "pgvector"` and `Store.Backend !=
        "postgres"` (the FK relationship requires both adapters share
        a pool over the same DB).
- [ ] Add `newBackends(cfg) (store.Store, vector.Index, func(), error)`
      in `cmd/reverb/main.go`. When both backends are
      `postgres`/`pgvector`:
      - Opens one `*pgxpool.Pool` from `Store.PostgresDSN`.
      - Calls `pgbackend.Migrate(ctx, pool, MigrateConfig{Dim, HNSWM,
        HNSWEFC})`.
      - Calls `postgres.New(ctx, pool, ...)` and
        `pgvector.New(ctx, pool, ...)`.
      - Returns the pair plus a shared `func()` cleanup that closes
        adapters first, then the pool (correct shutdown order in one
        place).
      - For non-postgres pairs, falls back to the existing
        `newStore(cfg)` / `newVectorIndex(cfg)` switch arms.
- [ ] Update `cmd/reverb/main.go:441` `validateEngine` for the
      postgres path. **Stay within the existing contract** (no
      embedder calls): ping pool, verify extension + version, verify
      embedded migration version matches the schema's `goose_db_version`
      head. Synthetic lookup is **not** added — it remains
      embedder-independent.
- [ ] `RUNBOOK.md` section "Operating Reverb on Postgres + pgvector":
      required Postgres + pgvector versions, required
      `CREATE EXTENSION` privilege, pool-sizing guidance, migration
      replay procedure, how to roll back a bad migration.

## 6. Integration test + split benchmarks

Goal: real pgvector exercised end-to-end in CI; benchmarks separated
so future regressions are attributable.

- [ ] Add `docker-compose.yml` service `pgvector` using
      `ankane/pgvector:v0.7.4` (confirm tag in Open Questions);
      bootstraps an empty `reverb` database.
- [ ] Add `make test-integration-pg` target that:
      1. Starts the container.
      2. Exports `REVERB_PG_DSN=postgres://reverb:reverb@localhost:5432/reverb?sslmode=disable`.
      3. Runs `go test ./pkg/store/postgres/... ./pkg/vector/pgvector/...
         ./pkg/store/conformance/... ./pkg/vector/conformance/...`.
      4. Tears down the container.
- [ ] Add `examples/pgvector-quickstart/`: a runnable program that
      stores three prompts (one with lineage), looks them up by exact
      and semantic tier, prints similarity + lineage. README explains
      `CREATE EXTENSION vector` and points at the migration command.
- [ ] Add three `BENCHMARKS.md` entries (split per the review):
      - `BenchmarkPGStore_GetByHash` — exact-tier read, in-pool.
      - `BenchmarkPGVector_Search` — k=10 against 100k-entry index,
        warm pool.
      - `BenchmarkPGCache_LookupEndToEnd` — full Reverb cache lookup
        with pgvector backend, fake embedder, 100k entries.
- [ ] Add a CI job `.github/workflows/pg-conformance.yml` that runs
      `make test-integration-pg` on every PR touching the new
      packages.

## 7. Docs & cross-cutting

Goal: backend lands cohesively across README, COMPATIBILITY,
CHANGELOG, roadmap, improvement plan.

- [ ] `README.md` "Backends" table: add Postgres (store) and
      pgvector (vector index) rows with links to package godoc.
- [ ] `CHANGELOG.md`:
      - "Added" — both new packages, the shared-pool wiring.
      - "Changed" — `vector.Index.Search` signature gained variadic
        `opts ...SearchOption` (pre-1.0 interface change; callers
        either pass new opts or pass nothing).
- [ ] `COMPATIBILITY.md`:
      - Migration guide for operators moving to pgvector. Prior
        cache entries do not migrate; either drain or accept rebuild
        (same shape as the redactor migration note in
        `COMPATIBILITY.md:286`).
      - Note that `vector.Index` implementations outside the
        standard library must adopt the variadic-opts signature.
- [ ] `specs/roadmap.md` item 1.10 annotated
      `— **DONE** (`spec/phase-1-pgvector`)` on merge.
- [ ] `docs/improvement_plan2.md` corresponding item annotated
      `✅ **DONE**` on merge (locate the pgvector-shaped row before
      annotating).
- [ ] Final stale-claim sweep against `README.md`, `COMPATIBILITY.md`,
      `CHANGELOG.md`. Doc-lint CI step from the prior two specs is
      still unimplemented — flagged again here but explicitly **not
      in scope** for this spec (it has been deferred twice; a
      dedicated micro-spec is the right home).

## Sequencing notes

- **Group 1 (interface change) lands first.** Every subsequent group
  references the new `Search(..., opts ...SearchOption)` signature.
- **Group 2 (`internal/pgbackend`) before Groups 3 and 4.** The
  adapters delegate to it; nothing in 3 or 4 should pre-empt
  shared-layer decisions.
- **Groups 3 and 4 can land in parallel** once Group 2 is in.
  Each has its own conformance suite; both gate independently.
- **Group 5 (binary wiring) follows 3 + 4.** Depends on adapter
  constructors being stable.
- **Group 6 (integration + benchmarks) before Group 7 (docs)** so the
  README and CHANGELOG cite real numbers.
- Within Group 2, write the migration **first**, run it against a
  scratch container, then build the query helpers on top of the
  resulting schema. Migrations are the contract.
