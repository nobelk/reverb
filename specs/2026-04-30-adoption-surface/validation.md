# Validation — Adoption Surface (Phase 1)

Phase: 1 · Date: 2026-04-30

## Exit criteria (from roadmap)

Verbatim from `specs/roadmap.md` §"Phase 1 exit criteria":

- Python and TypeScript SDKs published; OpenAPI spec on GitHub Pages.
- `reverb-cli` and admin UI shipped.
- Streaming, reverse-proxy, re-ranker, and singleflight available behind
  opt-in flags or new APIs.
- pgvector backend merged with conformance compliance.
- All "known gaps" called out in current docs are either fixed or
  explicitly removed from the doc.

This spec covers bullets 1, 2, and 5. Bullets 3 and 4 (streaming,
reverse-proxy, re-ranker, singleflight, pgvector) are deferred to the
follow-up Phase 1 spec listed under §"Non-goals" in `requirements.md`.
The full Phase 1 cannot be marked complete until both specs ship.

## Merge checklist

- [ ] All task groups in `plan.md` complete.
- [ ] **Tests green (unit):** `make test` passes from a clean checkout
      with `CGO_ENABLED=0`. No skipped tests outside the documented
      MCP-experimental and Docker-required sets.
- [ ] **Tests green (integration):** `make test-integration` passes
      against the standard Docker compose (Redis, NATS, Badger).
- [ ] **False-positive budget held:** `make bench-quality` reports
      `UnrelatedPairs ≤ 0/10` at the default similarity threshold of
      0.95 (`tech-stack.md` §"Quality and correctness commitments"). Any
      regression in this number blocks the merge.
- [ ] **Conformance suites green:** `pkg/store/conformance` and
      `pkg/vector/conformance` pass for all currently-shipped backends.
      No new backends are introduced in this spec, so this is a
      no-regression check.
- [ ] **Lint clean:** `make lint` reports zero violations.
- [ ] **Docs sweep complete:** `README.md`, `COMPATIBILITY.md`, and
      `CHANGELOG.md` contain no stale "known gap" / "not yet wired" /
      "TODO" lines outside the explicitly-experimental MCP surface. The
      doc-lint CI step (Group 4) is green.
- [ ] **Working example demonstrates a real semantic hit:**
      `examples/openai-chat/` runs end-to-end against either a real
      OpenAI key or a containerized Ollama, and prints — for the
      paraphrased prompt round — a similarity score above the threshold
      plus the source lineage of the cached entry.
- [ ] **OpenAPI authoritative:** `openapi/v1.yaml` is checked in,
      published to GitHub Pages, and the drift-check CI job is green.
      `tech-stack.md` §"Public surface" row for HTTP REST is updated to
      reflect the artifact's authoritative status.
- [ ] **Both SDKs published:** `pip install reverb` resolves to the
      0.1.0 release on PyPI; `npm install @reverb/client` resolves to
      0.1.0 on npm. Each sibling repo's CI smoke-tests against a live
      `cmd/reverb` instance.
- [ ] **`reverb-cli` published:** binary released on the `nobelk/reverb`
      GitHub releases page; Docker image tagged; Homebrew formula
      merged. `reverb-cli stats --server localhost:8080` works against
      a stock standalone server.
- [ ] **Admin UI reachable:** `cmd/reverb` started with `WithAdminUI()`
      serves `/_admin` and the test-query box performs a real lookup.

## How to verify

**Local verification before opening the merge PR.** From a clean
checkout of `spec/phase-1-adoption-surface`:

```sh
make lint
make test
make test-integration       # requires Docker
make bench-quality          # gates the false-positive budget
```

All four must exit zero. If `bench-quality` reports any non-zero
`UnrelatedPairs` count at threshold 0.95, stop and treat as a release
blocker per `mission.md` principle 1.

**End-to-end verification of the example.** With a `cmd/reverb` running
locally on port 8080:

```sh
cd examples/openai-chat
OLLAMA_HOST=http://localhost:11434 go run .
```

Expected output: round 1 logs `tier=miss`; round 2 logs
`tier=exact hash=...`; round 3 logs `tier=semantic similarity=0.9x
sources=[...]`. Failure of round 3 to produce a semantic hit means the
embedder integration regressed and blocks merge.

**Cross-repo verification.** Each sibling repo (`reverb-python`,
`reverb-js`, `reverb-ui`) runs its own CI against a containerized
`cmd/reverb` built from the merge candidate. The main-repo merge PR
links to the green CI run on each sibling. If any sibling is red, merge
the main-repo PR last (after the sibling fix), so a published 0.1.0 of
`reverb` / `@reverb/client` is never broken against the main-repo
release it targets.

**Docs spot-check.** Open `README.md`, `COMPATIBILITY.md`, and
`CHANGELOG.md`. Search for the strings `not yet`, `TODO`, `known gap`,
`experimental` (excluding the MCP block). Each remaining occurrence
must either be intentional (MCP) or get fixed before merge — the
doc-lint CI step automates this, but a human spot-check at PR time
catches phrasings the regex misses.
