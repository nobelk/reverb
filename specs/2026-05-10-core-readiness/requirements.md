# Requirements — Core Readiness (Phase 1 closeout, part 1)

Phase: 1
Date: 2026-05-10
Branch: spec/phase-1-core-readiness

## Context

The first Phase-1 spec (`specs/2026-04-30-adoption-surface/`) shipped the
adoption surface — OpenAPI 3.1, Python and TypeScript SDKs, `reverb-cli`,
`--validate`, the OpenAI-chat example, and the doc sweep. That landed on
`main` in PR #4 and unblocked non-Go consumers, but it left six Phase-1
roadmap items un-shipped: 1.6 streaming, 1.7 OpenAI-compatible reverse
proxy, 1.8 cross-encoder reranker, 1.9 singleflight, 1.10 pgvector backend,
1.11 PII redaction.

This spec covers **four of those six**. The remaining two (reranker, pgvector)
are deferred — see Non-goals. The four scoped items collectively lift the
library from "adoptable" to "production-ready depth": cache surface
coverage (streaming), high-leverage adoption (reverse proxy), a one-line
cost saver (singleflight), and the table-stakes regulated-industry hook
(PII redaction).

The persona target is unchanged from `specs/mission.md`: Go developers
building LLM-powered applications, with secondary inheritance by Python/TS
SDK consumers. The `mission.md` constraints that shape this spec most
directly are principle 1 (correctness over hit-rate — bears on streaming
chunk replay and PII redaction), principle 2 (zero required dependencies
for Quick Start — bears on the redactor default-off posture and the proxy
mode being opt-in), and principle 4 (library API stability — bears on the
streaming surface being additive).

## Scope

- **1.6 Streaming response support.** Add `Chunks []ResponseChunk` (delta +
  finish_reason) alongside the existing `ResponseText` on the response
  type. New endpoint `POST /v1/lookup-stream` returns SSE if cached. The
  existing `/v1/lookup` endpoint is unchanged.
- **1.7 OpenAI-compatible reverse-proxy mode.** New `cmd/reverb` flag
  `--proxy openai --upstream <url>`. Forwards on miss, caches on success,
  returns from cache on hit. Honors `Cache-Control: no-cache` for bypass.
  Lookup-only API remains the primary, recommended surface.
- **1.9 Singleflight on cache miss.** Coalesce concurrent fills via
  `golang.org/x/sync/singleflight`, exposed as
  `LookupOrCall(ctx, req, fillFn)` on `*reverb.Client`. The existing
  `Lookup` method is unchanged.
- **1.11 PII redaction hook in normalize pipeline.** New optional
  `Redactor` interface invoked between normalize and hash. Default
  regex-based redactor (emails, phones, credit-card patterns, SSN). Per-
  namespace toggle. Ships **disabled by default**; no new mandatory
  dependency on the core path.

## Non-goals

- **1.8 Cross-encoder reranker.** Deferred to Phase 2. The reranker
  introduces an ML-runtime opt-in build tag (CGO/ONNX) which is the
  heaviest single piece of new tech. Better paired with the Phase-2
  quality items it composes with — D2 adaptive thresholds and D4 hit-
  quality feedback loop — than with the surface/perf/security items in
  this spec.
- **1.10 Postgres + pgvector backend.** Deferred to a separate Phase-1
  spec (`specs/YYYY-MM-DD-pgvector/`, not yet scaffolded). New backends
  must pass the conformance suites under `pkg/store/conformance` and
  `pkg/vector/conformance`, and pgvector adds schema migrations on top
  of that — bundling it with the four items here would risk blocking the
  spec on backend churn.
- **Tightening the false-positive budget.** Without the reranker (1.8),
  this spec cannot raise the published budget. The 0/10 floor at
  threshold 0.95 stays in force; the move toward 0/100 happens in the
  Phase-2 quality spec.
- **The optional `LookupOrFetch` LLM-gateway mode** (roadmap §3.15). The
  reverse-proxy in 1.7 is OpenAI-API-shaped only; a generic `Provider`-
  shaped helper is a separate later item.

## Decisions

- **Streaming is additive only.** `Chunks []ResponseChunk` is a new field;
  `ResponseText` is unchanged. `/v1/lookup-stream` is a new endpoint;
  `/v1/lookup` is unchanged. Per `mission.md` principle 4 (library API
  stability is sacred), no breaking change to public types.
- **Reverse-proxy is opt-in via flag, not default behavior.** Inferred
  from `mission.md` Scope §"optional `LookupOrFetch`" — a gateway-shaped
  surface is allowed only as opt-in, with the lookup-only path remaining
  primary. The `--proxy` flag must default off; running `cmd/reverb`
  without it preserves today's behavior exactly.
- **PII redactor ships disabled by default.** Inferred from `mission.md`
  principle 2 (zero required dependencies for Quick Start). The default
  regex set (emails, phones, credit-card patterns, SSN) is in-process
  Go regex only — no external service, no model. Per-namespace toggle.
  Default-off so the Quick Start does not change.
- **Singleflight is a new method, not a behavior change.** `LookupOrCall`
  is added; `Lookup` semantics do not change. Callers opt in by switching
  call sites.
- **OpenAPI spec stays authoritative.** Per `specs/tech-stack.md` §Public
  surface, every new HTTP endpoint added in this spec must land in
  `openapi/v1.yaml` and pass `pkg/server/openapi_drift_test.go` before
  merge. This applies to `/v1/lookup-stream` and the proxy-mode shim.

### Open questions

- [ ] **SSE keepalive cadence** for `/v1/lookup-stream`: hard-code or
      configurable? Default proposal: 15s comment-line keepalives, no
      knob in the first cut.
- [ ] **Proxy mode auth pass-through.** When the proxy forwards on miss,
      does it pass the caller's `Authorization` header upstream verbatim,
      or use a server-side `OPENAI_API_KEY`? Default proposal: pass
      through; document that the upstream key is the caller's key.
- [ ] **Redactor application order.** Pre-normalize, post-normalize, or
      post-hash? Default proposal: between normalize and hash, so the
      hashed/stored prompt has PII already removed.
- [ ] **Singleflight scope.** Per-process only (default) or distributed
      via store-backed lock for cluster mode? Default proposal: per-
      process only. Distributed coalescing belongs to roadmap §3.6.

## References

- `specs/mission.md` — principles 1, 2, 4; Scope §"optional `LookupOrFetch`"
- `specs/tech-stack.md` — §Public surface (OpenAPI authoritative);
  §Pluggable interfaces (new `Redactor` follows interface-first rule);
  §Dependency policy (`golang.org/x/sync` for 1.9 singleflight is in the
  required-core set already as a transitive)
- `specs/roadmap.md` — Phase 1, items 1.6, 1.7, 1.9, 1.11
- `specs/2026-04-30-adoption-surface/` — first Phase-1 spec, shipped
- `docs/improvement_plan2.md` — items C1, C5, E1, G1 (the same four,
  cross-referenced by their plan IDs)
