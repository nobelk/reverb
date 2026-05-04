# Requirements — Adoption Surface (Phase 1)

Phase: 1
Date: 2026-04-30
Branch: spec/phase-1-adoption-surface

## Context

The Phase 0 baseline shipped a solid library: two-tier cache, lineage-driven
invalidation, three storage backends, two vector indices, three embedders,
three CDC listeners, HTTP/gRPC/MCP servers, and a published false-positive
budget (`specs/roadmap.md` §"Phase 0 — Foundation"). The library is sound;
the surface around it is thin. Per `specs/mission.md` §Audience, the primary
persona is Go developers, but the secondary personas — non-Go application
developers and platform/SRE teams — currently cannot adopt Reverb without
writing custom HTTP clients or operating it via raw `curl`. This spec closes
that gap.

The Phase 1 OKR target (`specs/roadmap.md` §OKRs) of 5 non-Go production
deployments is structurally blocked until SDKs and an operator CLI exist.
The OpenAPI 3.1 spec is the contract artifact that `tech-stack.md`
§"Public surface" identifies as graduating to authoritative once 1.1 ships,
so it sequences first.

## Scope

In scope for this spec:

- **1.1 OpenAPI 3.1 spec for `/v1/*`** — authored, published to repo and
  GitHub Pages. Becomes the authoritative HTTP contract on ship.
- **1.2 Python SDK** — `reverb` PyPI package wrapping `lookup`, `store`,
  `invalidate`, plus a `@cached_completion` decorator wrapping the OpenAI
  and Anthropic SDKs. Lives in a sibling `reverb-python` repo.
- **1.3 TypeScript / JavaScript SDK** — `@reverb/client` for Node and edge
  runtimes (Vercel, Cloudflare Workers). Same surface as the Python SDK.
  Lives in a sibling `reverb-js` repo.
- **1.4 `reverb-cli` operator binary** — separate Go binary (not bundled
  into the server) with subcommands `stats`, `lookup`, `store`,
  `invalidate <source>`, `evict --namespace`, `warm <jsonl>`, `export`,
  `import`, `validate-config`. Talks HTTP or gRPC.
- **1.12 Documentation sweep** — README, COMPATIBILITY, CHANGELOG. Remove
  the "metrics HTTP server not yet wired" caveat (it is wired now via
  `WithMetricsOnMux` + `NewMetricsServer`). Audit and either close or
  remove every "known gap" line that no longer reflects reality.
- **1.13 `--validate` flag in `cmd/reverb`** — `reverb --validate --config
  foo.yaml` parses the config, runs a sample lookup, exits non-zero on any
  failure. Referenced in `COMPATIBILITY.md` and the upgrade-testing
  checklist; currently unimplemented.
- **1.14 `examples/openai-chat/` with real semantic hits** — self-contained
  example using a real OpenAI key (or Ollama for offline testing) that
  demonstrates a semantic hit on a paraphrased prompt. Replaces the
  current pattern where every example uses the deterministic `fake`
  embedder and only ever shows exact hits.

## Non-goals

The following Phase 1 items are deferred to a follow-up spec
(`specs/<later>-phase-1-runtime-features/` or similar):

- 1.6 Streaming response support
- 1.7 OpenAI-compatible reverse-proxy mode
- 1.8 Cross-encoder re-ranker tier
- 1.9 Singleflight on cache miss
- 1.10 Postgres / pgvector backend
- 1.11 PII redaction hook in normalize pipeline

These are runtime/correctness features rather than adoption-surface work,
and bundling them into one spec would obscure the adoption-surface story.
They retain their Phase 1 scheduling — the deferral is editorial within
Phase 1, not a push to Phase 2.

The admin web UI at `/_admin` (originally tracked here as 1.5) has been
moved out of Phase 1 entirely. It now lives at roadmap §2.24, alongside
the Grafana dashboards (§2.3) and Prometheus alerts (§2.4) it composes
with. Phase 1 ships the operator CLI (1.4) as the standalone operator
surface; the graphical layer follows in Phase 2.

Also explicitly out of scope (per `specs/mission.md` §"Out of scope"):

- A managed/hosted Reverb offering — sibling repos publish to PyPI and
  npm; the maintainers do not run a control plane.
- Routing / model-selection logic in the SDKs — they are thin wrappers
  over the wire contract, not LLM gateways.

## Decisions

- **Use existing stack as-is.** No constitutional changes proposed by
  this spec. The work fits within current `tech-stack.md` commitments:
  Go-only main repo, CGO-free core, zero-dep Quick Start, additive HTTP
  evolution under `/v1/`.

- **JS/TS toolchains live in sibling repos.** Per `tech-stack.md`
  §"Repository composition" (constitutional row), the main `nobelk/reverb`
  repo contains only Go source. Concretely for this spec:
  - **`reverb-python`** — sibling repo for the Python SDK (1.2).
    Publishes the `reverb` package to PyPI.
  - **`reverb-js`** — sibling repo for the TypeScript SDK (1.3).
    Publishes `@reverb/client` to npm.

  The same constitutional rule will govern `reverb-ui` (admin UI) when
  that work lands in Phase 2 (roadmap §2.24); it is out of scope for
  this spec.

- **OpenAPI is the cross-language source of truth.** Both SDKs are
  generated from `openapi/v1.yaml` (via `openapi-generator` or equivalent),
  with thin language-idiomatic wrappers added by hand. Hand-rolled SDKs
  drift from the wire contract; generation prevents that.

## References

- `specs/mission.md` — §Audience (primary Go, secondary Python/TS via
  SDKs); §Scope ("Cross-language consumption via SDKs and adapters
  generated from a published OpenAPI 3.1 spec and the existing `.proto`
  contract"); principle 4 (library API stability).
- `specs/tech-stack.md` — §"Repository composition" (Go-only main repo,
  constitutional); §"Public surface" (OpenAPI graduates to authoritative
  on 1.1 ship); §"Deployment shapes" (SDKs add only thin language-
  idiomatic sugar over the wire contract).
- `specs/roadmap.md` — Phase 1 items 1.1–1.4, 1.12–1.14; Phase 1 exit
  criteria; OKR table (5 non-Go production deployments target). The
  admin UI originally listed as 1.5 has moved to §2.24.
