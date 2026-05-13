# Validation — Postgres + pgvector backend

Phase: 1 · Date: 2026-05-13

## Exit criteria (from roadmap)

Verbatim from `specs/roadmap.md` §"Phase 1 exit criteria":

> - pgvector backend merged with conformance compliance.

This spec satisfies that bullet in full. The remaining Phase-1 exit
criteria were met by the two prior Phase-1 specs. With this merged,
Phase 1 closes.

## Merge checklist

- [ ] All task groups in `plan.md` complete (Groups 1–7).
- [ ] **`vector.Index` interface change merged cleanly:** `flat`,
      `hnsw`, and all callers in `pkg/cache/semantic` compile against
      the new variadic-opts signature. No skipped tests in the
      existing vector or semantic suites.
- [ ] **Both conformance suites green against live pgvector:**
      `REVERB_PG_DSN=postgres://… go test ./pkg/store/postgres/...
      ./pkg/vector/pgvector/... ./pkg/store/conformance/...
      ./pkg/vector/conformance/...` passes. The Docker-compose path
      (`make test-integration-pg`) is the canonical local invocation
      and the CI workflow shape.
- [ ] **New `SearchScopedByNamespace` conformance case green for
      pgvector; passive for flat/hnsw** (the capability-gated test
      asserts post-filter equivalence for non-scoping backends).
- [ ] **False-positive budget held:** `make bench-quality` reports
      `UnrelatedPairs ≤ 0/10` at threshold 0.95 with pgvector wired
      as the vector index. Pre-filtering by namespace/model should
      *improve* this, not regress it — per `mission.md` principle 1,
      any non-zero count blocks merge.
- [ ] **Split latency baselines published:** `BENCHMARKS.md` contains
      `BenchmarkPGStore_GetByHash`, `BenchmarkPGVector_Search` (k=10,
      100k entries), and `BenchmarkPGCache_LookupEndToEnd`. Numbers
      reproducible against `ankane/pgvector:v0.7.4`.
- [ ] **Lint clean:** `make lint` reports zero violations across
      `internal/pgbackend`, `pkg/store/postgres`, `pkg/vector/pgvector`.
- [ ] **CGO-free build:** `CGO_ENABLED=0 go build ./...` succeeds.
- [ ] **`--validate` extends without breaking contract:** with a
      postgres-mode config, `reverb --validate --config postgres.yaml`
      pings the pool, verifies the `vector` extension and version,
      verifies migration version is current, and exits 0. It does
      **not** invoke the embedder. The pre-existing
      `cmd/reverb/main_test.go --validate` coverage continues to
      pass with the new path added.
- [ ] **Migrations are explicit:** importing `pkg/store/postgres` or
      `pkg/vector/pgvector` and calling `New()` against a fresh DB
      with no `reverb` schema returns a typed
      "schema not migrated" error. Operators must call
      `pgbackend.Migrate(ctx, pool, cfg)` first. Verified by a
      dedicated test.
- [ ] **Shared pool ownership lives in one place:** `cmd/reverb/main.go`
      `newBackends(cfg)` is the single owner of pool open/close for
      the postgres/pgvector pair; `newStore` / `newVectorIndex` no
      longer have `case "postgres"` / `case "pgvector"` arms.
- [ ] **Docs sweep complete:** `README.md` Backends table updated,
      `CHANGELOG.md` "Added" + "Changed" entries present,
      `COMPATIBILITY.md` migration guide + interface-change note
      present, `RUNBOOK.md` "Operating on pgvector" section present.
- [ ] **Roadmap + improvement-plan annotated:** `specs/roadmap.md`
      item 1.10 marked `— **DONE**`; `docs/improvement_plan2.md`
      pgvector item annotated `✅ **DONE**`.

## How to verify

**Local verification before opening the merge PR.** From a clean
checkout of `spec/phase-1-pgvector`:

```sh
make lint
go test ./pkg/vector/... ./pkg/cache/semantic/...   # interface change
go test ./internal/pgbackend/... ./pkg/store/postgres/... ./pkg/vector/pgvector/...
make test-integration-pg                            # gated conformance
make bench-quality                                  # FP budget
CGO_ENABLED=0 go build ./...                        # CGO invariant
```

All six must exit zero. The `bench-quality` step is the gate per
`mission.md` principle 1.

**Interface-change spot-check.** The `pkg/vector/conformance` suite
gains a `SearchScopedByNamespace` test that is **capability-gated**.
Run it against all three vector backends:

```sh
go test ./pkg/vector/flat/...     -run TestConformance/SearchScopedByNamespace
go test ./pkg/vector/hnsw/...     -run TestConformance/SearchScopedByNamespace
REVERB_PG_DSN=... go test ./pkg/vector/pgvector/... -run TestConformance/SearchScopedByNamespace
```

For `flat` and `hnsw`, the test asserts that the semantic-cache
post-filter still produces correct results (because the impls ignore
the opts). For `pgvector`, it asserts the pre-filter eliminates
wrong-namespace candidates entirely at the index layer.

**End-to-end verification.** With `make test-integration-pg`'s
container running:

```sh
export REVERB_PG_DSN="postgres://reverb:reverb@localhost:5432/reverb?sslmode=disable"

# One-time setup: run migrations explicitly (not auto on New).
go run ./internal/pgbackend/cmd/migrate -dsn "$REVERB_PG_DSN" -dim 1536 -hnsw-m 16 -hnsw-efc 64

cd examples/pgvector-quickstart
go run .
```

Expected output: round 1 logs `tier=miss` (cold cache; one row in
`entries`, one in `embeddings`, one in `entry_sources`); round 2 logs
`tier=exact hash=…` (read from `entries.prompt_hash` via the
exact-tier index); round 3 logs `tier=semantic similarity=0.9x
sources=[…]` (HNSW result with lineage preserved through the
`entry_sources` join). Round 3 failure means the HNSW index is
mis-tuned or pre-filtering eliminated the candidate — blocks merge.

**Operator-path verification.** Build the standalone binary, point
at a fresh Postgres instance:

```sh
./bin/reverb --validate --config examples/pgvector-quickstart/reverb.yaml
echo $?    # must print 0; must NOT have invoked the embedder
```

The `--validate` contract is unchanged: pool reachability + schema
prereqs + migration version, no embedder roundtrip. The existing
`cmd/reverb/main_test.go` coverage must still pass alongside the new
postgres path.

**Docs spot-check.** Open `README.md` Backends table — Postgres and
pgvector rows present with package godoc links. Open `CHANGELOG.md` —
"Added" entry for the new packages **and** "Changed" entry calling
out the variadic-opts on `vector.Index.Search`. Open
`COMPATIBILITY.md` — migration guide for the backend change and the
interface-change note for third-party `vector.Index` implementations.
Run `grep -niE 'TODO|not yet wired|known.gap' README.md
COMPATIBILITY.md CHANGELOG.md`; only the `Known gaps: (None)` line
and intentional MCP-context occurrences should match.
