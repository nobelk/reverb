# Plan — Adoption Surface (Phase 1)

Phase: 1 · Date: 2026-04-30

## 1. Wire contract

Goal: publish OpenAPI 3.1 as the cross-language source of truth so SDKs
can be generated rather than hand-rolled.

- [x] Author `openapi/v1.yaml` covering every `/v1/*` endpoint exposed by
      `pkg/server/http`. Include request/response schemas, error envelope,
      auth (`Authorization: Bearer …`), and rate-limit headers.
- [x] Wire a CI check that diffs `openapi/v1.yaml` against the live HTTP
      handlers (round-trip a sample request through both and assert
      schema match) so the spec cannot silently drift.
      — Implemented as `pkg/server/openapi_drift_test.go`.
- [x] Publish to GitHub Pages via a `.github/workflows/openapi.yml`
      job that renders Swagger UI at `https://nobelk.github.io/reverb/`.
- [x] Cross-link from `README.md` → "HTTP API contract" → GitHub Pages.
      — `README.md:102`.
- [x] Update `specs/tech-stack.md` §"Public surface" row for HTTP REST to
      mark the OpenAPI artifact authoritative (was: "Go handler is
      authoritative until OpenAPI ships").
      — `specs/tech-stack.md:22`.

## 2. Language SDKs (sibling repos)

Goal: ship Python and TypeScript SDKs published to PyPI and npm so non-Go
users can `pip install` / `npm install` and reach the cache in five lines.

- [ ] Create `nobelk/reverb-python` sibling repo. Generate the wire client
      from `openapi/v1.yaml` via `openapi-generator-cli` (Python target).
      — **Deferred:** SDK currently lives in this repo under `sdk/python/`
      (generator at `sdk/python/openapi-generator/`). RUNBOOK.md
      §"SDK Release Coordination" tracks the pending migration to the
      sibling repo.
- [x] Hand-roll a thin `reverb` package on top of the generated client
      exposing `lookup`, `store`, `invalidate`, plus a `@cached_completion`
      decorator that wraps `openai` and `anthropic` SDK calls.
      — `sdk/python/reverb/{client.py,decorators.py}` (v0.1.0rc0).
- [x] Publish `reverb` 0.1.0 to PyPI. Add a smoke-test script that runs
      against a `make run-server` instance in CI.
      — `.github/workflows/sdk-python.yml` `smoke:` job.
- [ ] Create `nobelk/reverb-js` sibling repo. Generate the wire client
      from `openapi/v1.yaml` (TypeScript / `fetch` target — no Node-only
      deps so it runs on Cloudflare Workers and Vercel Edge).
      — **Deferred:** SDK currently lives in this repo under `sdk/js/`
      (generator at `sdk/js/openapi-generator/`). Migration tracked
      alongside the Python SDK in RUNBOOK.md.
- [x] Hand-roll the `@reverb/client` surface (same shape as the Python SDK
      where idiomatic). Ship as ESM + CJS dual package.
      — `sdk/js/src/{client.ts,decorators.ts}`; `tsup.config.ts` builds dual ESM/CJS.
- [x] Publish `@reverb/client` 0.1.0 to npm. Smoke test from a Node and a
      Workers runtime in CI.
      — `.github/workflows/sdk-js.yml` `smoke:` job runs `smoke:node` and `smoke:workers`.
- [x] Add release-coordination notes to `RUNBOOK.md`: when the OpenAPI
      spec changes in the main repo, the two sibling repos must
      regenerate and tag a matching minor before the main-repo release
      goes out.
      — `RUNBOOK.md:582` "SDK Release Coordination" section.

## 3. Operator UX

Goal: give operators a CLI for the workflows that today require `curl` or
custom Go code.

- [x] Add `cmd/reverb-cli/` (separate `main` package, separate binary —
      do not bundle into `cmd/reverb`). Subcommands per roadmap §1.4:
      `stats`, `lookup`, `store`, `invalidate <source>`,
      `evict --namespace`, `warm <jsonl>`, `export`, `import`,
      `validate-config`. The three endpoints with no current `/v1/*`
      surface (`evict`, `export`, `import`) ship as honest stubs that
      exit `64` (`EX_USAGE`) with a pointer to this spec — they
      graduate once the matching server endpoints land in
      `openapi/v1.yaml`.
- [x] CLI talks HTTP by default; gRPC via `--transport grpc` flag.
      Reuse the wire types from `pkg/server/proto`.
- [x] Ship a `reverb-cli` Docker image (`Dockerfile.cli`, distroless
      static base) and a release workflow (`.github/workflows/release-cli.yml`)
      that cross-builds linux/darwin/windows binaries on every `v*.*.*`
      tag and publishes a multi-arch image to GHCR. The Homebrew tap
      formula is deferred to the first tagged release — it has to point
      at real release-asset URLs, which only exist post-tag.
- [x] Document the CLI in `docs/cli.md` with one example per subcommand.

> **Deferred:** The graphical admin UI at `/_admin` originally tracked here
> has moved to roadmap §2.24 (Phase 2). Phase 1 ships the CLI alone; the
> UI lands once the dashboards (§2.3) and alerts (§2.4) it composes with
> are also in place.

## 4. Honesty & testability

Goal: bring the docs back in line with shipped reality and add the
`--validate` operator workflow that other docs already reference.

- [x] `cmd/reverb`: implement `--validate` flag. Parse config, construct
      the engine with all wired-up backends, run a `lookup` against a
      synthetic prompt, exit 0 on success or non-zero with a structured
      error report. Cover with a CLI integration test.
      — Implemented in `cmd/reverb/main.go` (engine constructed without
      binding listeners; store connectivity exercised via `Stats()`).
- [x] Update `COMPATIBILITY.md` upgrade-testing checklist to actually
      reference `reverb --validate` rather than describing it as future
      work.
      — `COMPATIBILITY.md:262` references the flag in the upgrade-testing
      checklist.
- [x] Sweep `README.md`, `COMPATIBILITY.md`, `CHANGELOG.md` for stale
      "known gap" / "not yet wired" claims. The metrics HTTP server is
      wired now (`WithMetricsOnMux` + `NewMetricsServer`); update text
      and add a regression test asserting `/metrics` is served when the
      option is set.
      — CHANGELOG `Known gaps: (None)`; COMPATIBILITY.md lists Metrics as
      Beta with the `metrics.addr` listener documented; README §metrics
      reflects shipped behavior.
- [ ] Add a doc-lint CI step that greps for forbidden strings (`TODO`,
      "not yet wired", "experimental" outside the MCP context, etc.) in
      `README.md`, `COMPATIBILITY.md`, `CHANGELOG.md` so future drift is
      caught at PR time.
      — **Not implemented:** no doc-lint step in `.github/workflows/`.
      Without this, future stale-claim drift is only caught by manual
      sweeps. Recommend adding before the next minor release.

## 5. Demonstration

Goal: prove the semantic-cache value proposition with a runnable example
that any new user can execute end-to-end.

- [x] Create `examples/openai-chat/`. Self-contained Go program that
      reads `OPENAI_API_KEY` (or `OLLAMA_HOST` for offline mode), wires
      Reverb in front of the OpenAI Go SDK via the
      `pkg/embedding/openai` (or `pkg/embedding/ollama`) provider plus
      `pkg/store/memory` and `pkg/vector/flat`.
- [x] Script that runs three rounds: (a) cold cache, exact prompt — miss;
      (b) same exact prompt — exact hit; (c) paraphrased prompt —
      semantic hit, with similarity and lineage printed.
- [x] `examples/openai-chat/README.md` with copy-paste setup, expected
      output, and a one-liner explaining why the paraphrase round is the
      thing that makes Reverb interesting.
- [x] Add the example to the "examples" gallery in the main `README.md`.
      — `README.md:231` lists it alongside the other examples; full
      walkthrough at `README.md:270` "OpenAI / Ollama paraphrase demo".
- [x] CI: run the example in offline (Ollama) mode against a containerized
      Ollama with a small embedder model, asserting the third round
      produces a semantic hit.
      — `.github/workflows/example-openai-chat.yml`.

## Sequencing notes

- **Group 1 (OpenAPI) blocks Group 2 (SDKs).** Don't start either SDK
  until `openapi/v1.yaml` is checked in and the drift-check is green —
  otherwise the generated clients are tied to a moving target and the
  sibling repos churn.
- **Groups 3, 4, 5 are independent of 1 and 2** and can run in parallel.
  The `reverb-cli` (Group 3), `--validate` flag (Group 4), and example
  (Group 5) all use the existing Go API; they don't need the OpenAPI
  artifact to land first.
- **Group 5 (example) lands last** because it transitively depends on
  the docs sweep in Group 4 (the example's README cross-links to
  README.md sections updated there) and benefits from being the
  user-facing capstone of the spec.
- Within Group 2, Python ships before TypeScript: it has the larger
  audience (`mission.md` §Audience) and `@cached_completion` is the
  most opinionated piece, so getting it right first informs the TS
  shape.
