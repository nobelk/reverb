# Reverb Source Simplification Plan

> Note: the user asked about `src/`, but Reverb follows the standard Go layout —
> source lives under `cmd/`, `internal/`, and `pkg/`. This plan covers all three.
> Generated code (`pkg/server/proto/*.pb.go`, ~1188 lines) is **out of scope**;
> it is regenerated from `.proto` files.

## Goals & non-goals

**Goals**
- Eliminate cross-package duplication that has accumulated organically.
- Lean into idiomatic Go 1.22+ features (the module is on `go 1.25`, so all
  recent stdlib/language features are fair game).
- Preserve all observable behavior: HTTP/gRPC/MCP wire formats, Prometheus
  metric names, OTel span names + attributes, error semantics, and persisted
  data layout (Badger keys, Redis keys, JSON encodings).
- Keep the public `pkg/reverb` API stable. The semantic-versioned surface
  (`Client`, `Config`, `Option`, `LookupRequest`, `StoreRequest`, `Stats`) does
  not change.

**Non-goals**
- No architectural rework (no new abstractions, no change to the tier
  hierarchy, no new vector backends).
- No new features. No performance optimizations beyond what falls out for free.
- No change to the proto schema, CLI flags, env vars, or YAML config keys.

## Verification strategy (applies to every phase)

After each phase:
1. `go build ./...`
2. `go vet ./...`
3. `go test ./... -race -count=1`
4. `go test ./pkg/store/conformance/...` and `go test ./pkg/vector/conformance/...`
   — these are the cross-implementation consistency suites.
5. Manual diff of OTel span names/attributes via the existing
   `pkg/store/memory/otel_test.go` and `pkg/reverb/otel_test.go`.
6. `go test ./benchmark/...` to confirm no perf regression.

If any phase changes a public symbol, run `go list -deps ./cmd/reverb` and
`gofmt -l` to make sure callers compile clean.

---

## Phase 1 — Mechanical de-duplication (low risk, high signal)

These are the "every reviewer would approve" wins. They are independent of one
another and can be landed in any order.

### 1.1 Collapse the four duplicate `Clock` interfaces

**Current state** (verified via grep):
- `pkg/reverb/config.go:11` — `type Clock interface { Now() time.Time }` + `realClock`
- `pkg/cache/exact/exact.go:17` — same
- `pkg/cache/semantic/semantic.go:19` — same
- `pkg/limiter/limiter.go:30` — same
- `internal/testutil/clock.go` — `FakeClock` (implements all four implicitly)

Four byte-identical declarations of a 1-method interface plus its trivial
real-time implementation.

**Action**: introduce `internal/clock/clock.go`:

```go
package clock

import "time"

type Clock interface { Now() time.Time }

type realClock struct{}
func (realClock) Now() time.Time { return time.Now() }

func Real() Clock { return realClock{} }
```

Replace each duplicate with `import clk "github.com/nobelk/reverb/internal/clock"`
and use `clk.Clock` / `clk.Real()`. Keep `reverb.Clock` as a `type Clock = clk.Clock`
alias so the public API is preserved.

**Why `internal/`**: `Clock` is plumbing, not part of Reverb's external SDK
contract. Putting it in `internal/` enforces that at the compiler level.

**LOC saved**: ~24, mostly removed boilerplate.
**Risk**: trivial — interface satisfaction is structural in Go.

### 1.2 Move `cosineSimilarity` to one place

**Current state**:
- `pkg/vector/flat/flat.go:100` — 16-line function
- `pkg/vector/hnsw/hnsw.go:434` — byte-identical copy

**Action**: add `pkg/vector/similarity.go` exposing `CosineSimilarity` (or keep
unexported `cosineSimilarity` if no external caller needs it). Both indexes
import from the parent `vector` package, so this is a 3-line change in each
file plus one new file.

`★ Insight: Go does not have a "free function" namespace; the package itself
serves as the namespace. Putting math helpers in the `vector` package alongside
`SearchResult` and `Index` is the idiomatic placement — there's no need for a
new sub-package just for one function.`

**LOC saved**: 16.
**Risk**: zero (pure function, identical behavior).

### 1.3 Adopt the existing `metrics.Tracer` helpers in stores

**Current state**: `pkg/metrics/tracing.go:100-139` already defines
`StartStoreGetSpan`, `StartStorePutSpan`, `StartStoreDeleteSpan`,
`StartStoreDeleteBatchSpan`. None of the three store implementations use them.
Each store has its own per-method 4-line preamble:

```go
ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.X")
defer span.End()
span.SetAttributes(attribute.String("gen_ai.system", "reverb"),
                   attribute.String("gen_ai.cache.store.backend", "Y"), ...)
```

This appears 8× in `memory.go`, 8× in `redis.go`, 8× in `badger.go` — ~24
copies of the same shape, ~120 LOC of boilerplate.

**Action**: extend `metrics.Tracer` (or add a `metrics.StoreTracer` type) so
each store call site reduces to:

```go
ctx, span := tracer.StartStoreGetSpan(ctx, "memory", id)
defer span.End()
```

The store struct gains a `tracer *metrics.Tracer` field, set in `New()` to
`metrics.NewTracer()` by default and override-able for tests. Pass the backend
label ("memory" | "redis" | "badger") as a constructor argument so the helper
can attach `gen_ai.cache.store.backend` itself.

**Constraint**: existing OTel spans (names *and* attributes) must remain
identical — the `pkg/store/memory/otel_test.go` test asserts on exact span
shape. Lift attributes verbatim into the helpers.

**Refactor `metrics.RecordError`** (already exists at line 152) to be the only
error-recording path; remove the inline `span.RecordError(err); span.SetStatus(...)`
duplicates currently in `redis.go:118-119, 124-125`, `badger.go`, and
`exact.go:57-59, 92-94`.

**LOC saved**: ~150.
**Risk**: medium-low. Tests are the safety net. Watch for
`pkg/reverb/otel_test.go` and `pkg/store/memory/otel_test.go`.

### 1.4 Delete dead/unused symbols

| Symbol | Location | Action |
|---|---|---|
| `hashutil.SHA256` | `internal/hashutil/hash.go:8` | No production caller. Delete. |
| `hashutil.ContentHash` | `internal/hashutil/hash.go:27` | No production caller (grep confirms). Delete; tests can call `sha256.Sum256` inline. |
| `cache.TierExact`, `cache.TierSemantic` | `pkg/cache/tier.go` | Defined but unused — `client.go` writes `"exact"`/`"semantic"` as string literals. Either adopt the constants in `client.go:418, 425, 447, 454, 462` *or* delete `pkg/cache/tier.go` entirely. Recommend: **adopt the constants** so the strings are typo-proof. |
| `Invalidator.Run` | `pkg/lineage/invalidator.go:126-162` | Only called from `invalidator_test.go:179`. The production loop is `Client.invalidationLoop` (`pkg/reverb/client.go:185`). Delete `Run`; rewrite that one test to drive `ProcessEvent` directly (which is what `Run` does in a loop). |
| `errFakeEmbeddingFailure` struct | `pkg/embedding/fake/fake.go:73-75` | Replace with `errors.New("fake embedding failure")` and `var ErrFakeEmbeddingFailure = errors.New(...)`. Custom error type adds nothing. |

**LOC saved**: ~50.
**Risk**: zero for the deletes; the constants change is mechanical.

### 1.5 Replace `interface{}` with `any` and `sort.Slice` with `slices.SortFunc`

The module is on `go 1.25` but predates `any` and `slices` in places.

| Location | Change |
|---|---|
| `pkg/vector/hnsw/hnsw.go:460-466, 477-483` (heap Push/Pop signatures) | `interface{}` → `any` |
| `pkg/store/redis/redis.go:207` | `[]interface{}` → `[]any` |
| `pkg/vector/flat/flat.go:71-73` | `sort.Slice` → `slices.SortFunc` returning `int` (use `cmp.Compare` for descending) |
| `pkg/vector/hnsw/hnsw.go:196-198, 347-349, 386-388` | same |

**Bonus** (Phase 2 candidate): `hnsw.go:347-349` re-runs `cosineSimilarity` *twice
per pair* during sort — once at line 348 for `out[i]`, once for `out[j]`. The
score is already in the heap entries that produced `out`. Keep `[]scoredNode`
through `searchLayer` and sort by `score` directly. Behavior identical, work
halved on the hot path.

**LOC**: minor. **Risk**: low. **Bonus**: measurable hot-path win in HNSW search.

---

## Phase 2 — Targeted refactors (medium risk, real clarity wins)

### 2.1 Tighten `pkg/reverb/client.go`'s nested drain loop

**Current state**: `client.go:213-243` has a `for { select { ... } }` whose
`<-c.ctx.Done()` case opens a *second* nested `for { select }` to drain `ch`.
Functionally correct but hard to read.

**Action**: factor the per-event work into a method:

```go
func (c *Client) processChange(ev cdc.ChangeEvent) {
    n, err := c.invalidator.ProcessEvent(c.ctx, lineage.ChangeEvent{
        SourceID: ev.SourceID, ContentHash: ev.ContentHash, Timestamp: ev.Timestamp,
    })
    if err != nil { c.logger.Error(...); return }
    c.collector.Invalidations.Add(int64(n))
}
```

Then the loop body collapses. The drain-on-shutdown phase becomes a single
non-blocking `for-select-default` instead of nested `for { for }`.

**LOC**: trivial. **Risk**: low — covered by `pkg/reverb/restart_test.go`.

### 2.2 Remove the unused `Invalidator.Run` after 1.4

The Client's invalidation loop and `Invalidator.Run` are duplicates of the
batch-flush pattern. After 1.4, the codebase has only one. Verify by grepping:
`grep -rn "Invalidator.*\.Run\|invalidator\.Run" --include="*.go"` should find
zero hits in non-test code (it currently finds zero hits in production code,
one in a test).

### 2.3 Make `searchLayer` keep its scores

After 1.5's bonus, `pkg/vector/hnsw/hnsw.go:286-352` should return
`[]scoredNode` (or `[]vector.SearchResult`) instead of `[]*node`, eliminating
the redundant cosine recomputation in the final sort. Calls in `Add()`
(line 131) and `Search()` (line 185) need to be adapted; in `Add()` the
caller only needs the `*node`s, but pulling out `.n` is cheap.

### 2.4 Switch `internal/retry` to `math/rand/v2`

`internal/retry/retry.go:40` uses the old `math/rand` package's global source.
In `math/rand/v2`, `rand.Float64()` is fast, lock-free per-goroutine, and the
canonical choice in Go 1.22+.

```go
// before
jitter := 1.0 + (rand.Float64()*2-1)*cfg.JitterFrac
// after  (import "math/rand/v2")
jitter := 1.0 + (rand.Float64()*2-1)*cfg.JitterFrac
```

Same call site, different import. `rand.Int63()` in `pkg/vector/hnsw/hnsw.go:71`
seed setup similarly migrates to `rand/v2.Uint64()` style. Since HNSW
constructs its own `rand.Rand` (line 71), keep that property — `math/rand/v2`
provides `rand.New(rand.NewPCG(...))`.

**Risk**: low. The retry behavior is statistically equivalent.
**Caveat**: `math/rand/v2` does not expose `Float64()` with a default global —
it requires constructing a source. For `retry.Do`, that means storing one
`*rand.Rand` per `Config` or using `rand.Float64()` from `math/rand/v2` (which
*does* exist as a package-level function in v2, backed by per-goroutine state).
Verify the API before committing.

### 2.5 Make the webhook listener context-aware on send

`pkg/cdc/webhook/webhook.go:145` does `events <- event` unconditionally —
blocks forever if the channel is full and the consumer has stopped reading.
Compare to `polling.go:96-100`, which selects on `<-ctx.Done()` too.

**Action**: wrap in `select { case events <- event: case <-r.Context().Done(): }`
and on cancellation return 503 to the caller. **Risk**: low. **Behavior change**:
the webhook now declines work when shutting down rather than hanging the HTTP
handler. Acceptable improvement.

### 2.6 Use `min`/`max` builtins where they clean up code

`pkg/vector/hnsw/hnsw.go:125-128`:
```go
topLayer := level
if idx.maxLayer < topLayer { topLayer = idx.maxLayer }
// → topLayer := min(level, idx.maxLayer)
```

`pkg/vector/hnsw/hnsw.go:182-184`:
```go
ef := idx.cfg.EfSearch
if ef < k { ef = k }
// → ef := max(idx.cfg.EfSearch, k)
```

`pkg/vector/flat/flat.go:75-77` and `pkg/vector/hnsw/hnsw.go:200-202`:
```go
if len(candidates) > k { candidates = candidates[:k] }
// → candidates = candidates[:min(len(candidates), k)]
```

**Skip** for `limiter.go:96` — that condition guards against `+Inf` and `NaN`,
which `min` does not handle the way you want. Keep the explicit check.

**LOC**: 6-8 net. **Risk**: zero.

---

## Phase 3 — Larger structural cleanups (medium-high risk, opt-in)

These are real refactors. Land Phase 1 + 2 first; revisit these only if the
team wants further consolidation.

### 3.1 Extract a shared `embeddingHTTPProvider` for OpenAI + Ollama

`pkg/embedding/openai/openai.go:99-176` and `pkg/embedding/ollama/ollama.go:83-144`
both: marshal a request body, build an HTTP request, set headers, call `Do`,
read response with a 10 MiB cap, branch on status, unmarshal, attach OTel
attributes. Roughly 60 lines × 2 = 120 lines, of which ~80 lines are
identical.

**Action**: introduce `pkg/embedding/internal/httpcall.go`:

```go
type Call[Req any, Resp any] struct {
    Method, URL string
    Headers     map[string]string
    Body        Req
    SpanName    string
}

func Do[Req any, Resp any](ctx context.Context, c *http.Client, call Call[Req, Resp]) (Resp, error)
```

Then `openai.doEmbed` and `ollama.doEmbed` shrink to ~15 lines each — pure
request/response shape declarations.

**Risk**: medium. Two small mistakes can change error wrapping or OTel
attribute shape. Walk the unit tests carefully: `openai_test.go` and
`ollama_test.go`.

**Skip if**: a third HTTP-backed embedder is unlikely to land. DRY across two
implementations is a tossup; across three it's clearly worth it. With only two
today, this is judgment-call territory.

### 3.2 Consider whether `pkg/reverb` Options should stay options

Counterpoint to the Explore agent's Phase 1 suggestion: the functional-options
pattern at `pkg/reverb/options.go` is *canonical Go for this exact situation*.
`reverb.New(cfg, embedder, store, vi, opts...)` has four required dependencies
and a handful of optional ones (logger, tracer, prom collector, etc.).
Replacing this with a giant config struct hurts readability.

**Decision**: keep the options as-is. Document this explicitly in the plan so
the question doesn't get re-asked.

### 3.3 Encapsulate the HNSW heap with generics

`pkg/vector/hnsw/hnsw.go:455-483` defines `candidateHeap` and `resultHeap` —
two near-identical heap types differing only by their `Less` direction.
`container/heap` interface still requires `any` Push/Pop, but a small generic
wrapper around `container/heap` can hide the casts:

```go
type orderedHeap[T any] struct { data []T; less func(a, b T) bool }
func (h *orderedHeap[T]) Len() int                { return len(h.data) }
func (h *orderedHeap[T]) Less(i, j int) bool      { return h.less(h.data[i], h.data[j]) }
// ... etc
```

Saves ~25 LOC and removes 4 type assertions. **Risk**: low if the test suite
covers the search path well (which it does, via
`pkg/vector/conformance/conformance.go`).

### 3.4 Lift the `Clock` parameter out of `exact.New` and `semantic.New`

After 1.1, `Cache` constructors still take a `Clock`. Since `clk.Real()` is a
default they could fall back to, the constructor signature is just clutter for
production callers. Two options:

- **(a)** Add `WithClock` functional options on these caches too. Heavier.
- **(b)** Make `Clock` a settable field on `Config`-shaped struct. Lighter.

**Recommendation**: (b) — add `Clock` to `semantic.Config` (already exists)
and a new `exact.Config`. Keep current constructors as wrappers for one
release.

---

## Phase 4 — Things to leave alone

To preempt premature cleanup, these are deliberately *not* on the list:

- **`throttled.New` returning `inner` when `cl == nil`** (`pkg/embedding/throttled/throttled.go:28-30`).
  This is the right idiom — callers always wrap, branchless.
- **`normalize.go`'s fixpoint loop** at line 29-35. Reads cleanly, terminates,
  is correct.
- **`pkg/auth/auth.go`'s prehashed-keys design**. SHA-256 + constant-time
  compare is exactly the canonical pattern.
- **`pkg/limiter` shape**. Both `TokenBucket` and `ConcurrencyLimiter` are
  small, well-commented, and do not duplicate stdlib (no `golang.org/x/time/rate`
  on the dep list, intentionally — keeps the dependency surface tight).
- **The `Clock` *type alias* for `reverb.Clock`** — public API stability.

---

## Sequencing & estimated effort

| Phase | LOC delta | Effort | Risk | Order |
|---|---|---|---|---|
| 1.1 Clock dedup | -25 | 1h | low | 1 |
| 1.2 cosineSimilarity dedup | -16 | 15m | none | 2 |
| 1.3 Tracer adoption in stores | -150 | 3h | medium-low | 3 |
| 1.4 Dead code removal | -50 | 30m | none | 4 |
| 1.5 `any` + `slices.SortFunc` | ±0 | 30m | low | 5 |
| 2.1 Drain-loop cleanup | -10 | 30m | low | 6 |
| 2.2 Drop `Invalidator.Run` | -40 | 30m | low (covered by 1.4) | with 1.4 |
| 2.3 Preserve heap scores | -5 | 1h | low | 7 |
| 2.4 `math/rand/v2` | ±0 | 30m | low | 8 |
| 2.5 Webhook ctx-aware send | +3 | 30m | low | 9 |
| 2.6 `min`/`max` builtins | -8 | 15m | none | 10 |
| 3.1 Embedding HTTP DRY | -80 | 4h | medium | optional |
| 3.3 Generic heap | -25 | 2h | low | optional |
| 3.4 Cache constructor cleanup | ±0 | 1h | low | optional |

**Total expected savings, Phase 1+2**: ~300 LOC removed (≈2% of hand-written
code) — but the bigger payoff is consistency: one Clock type, one
cosineSimilarity, one OTel-span pattern, one batch-flush loop. Each future
contributor sees one canonical way to do each thing.

## What success looks like

After Phase 1+2 lands:

1. `grep -rn "type Clock interface" --include="*.go"` returns one hit.
2. `grep -rn "cosineSimilarity" --include="*.go" | wc -l` drops from 11 to ~9
   (one definition + callers, no duplicate definition).
3. `grep -rn "otel.Tracer(tracerName).Start" pkg/store/` returns zero hits —
   all stores route through `metrics.Tracer` helpers.
4. `grep -rn "interface{}" --include="*.go"` is restricted to `*.pb.go` and a
   handful of unavoidable-by-stdlib spots.
5. Test suite runtime unchanged (≤5% delta).
6. `go test ./pkg/store/conformance/...` and `./pkg/vector/conformance/...`
   pass without modification — no behavior changed.
7. The public Reverb API (`pkg/reverb`, `pkg/server`) is byte-compatible: any
   external user's code still compiles.
