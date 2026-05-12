# Validation — Core Readiness (Phase 1 closeout, part 1)

Phase: 1 · Date: 2026-05-10

## Exit criteria (from roadmap)

This spec covers four of the six items the `roadmap.md` Phase 1 exit
criteria require. Verbatim from `specs/roadmap.md` §"Phase 1 exit criteria":

> - Streaming, reverse-proxy, re-ranker, and singleflight available
>   behind opt-in flags or new APIs.

This spec satisfies **streaming, reverse-proxy, and singleflight**. The
re-ranker is deferred to Phase 2 per the requirements §Non-goals; the
remaining Phase-1 exit-criteria items (pgvector backend; PII redaction is
not actually called out in Phase 1's exit criteria but is required by
roadmap item 1.11) are covered by 1.11 here and a sibling pgvector spec
respectively.

The full Phase-1 exit criteria are not satisfied until both this spec
and the pgvector spec land.

## Merge checklist

- [ ] All task groups in `plan.md` complete (Groups 1–5)
- [ ] Tests green: `go test ./...`, including the new
      `pkg/reverb/singleflight_test.go` stress test, the streaming
      integration test, the PII-redactor benchmark, and the
      `pkg/store/conformance` + `pkg/server/openapi_drift_test.go`
      suites
- [ ] `examples/openai-proxy/` and `examples/pii-redaction/` runnable
      end-to-end; `make examples` (or equivalent) green
- [ ] CHANGELOG.md updated with one "Added" entry per feature group;
      "Security" section noting the redactor; "Changed" entries only
      where strictly required (recall: Group 1 streaming is additive)
- [ ] COMPATIBILITY.md updated with migration notes for streaming
      (additive-only, no migration required) and the redactor
      (one-time invalidation expected on enable)
- [ ] README.md updated: streaming + proxy callouts in the feature
      list; pointer to `examples/openai-proxy/` in the gallery
- [ ] `openapi/v1.yaml` updated and drift test green
- [ ] Python (`sdk/python/`) and TypeScript (`sdk/js/`) SDKs regenerated
      and smoke jobs green for streaming
- [ ] `specs/roadmap.md` Phase-1 items 1.6, 1.7, 1.9, 1.11 marked
      shipped
- [ ] `docs/improvement_plan2.md` items C1, C5, E1, G1 annotated with
      the same DONE pattern used in the prior post-merge sweep

## How to verify

**Streaming.** Run `cmd/reverb` in default mode and a small Go program
that calls `Client.Store` with a `Chunks` payload, then `curl -N
http://localhost:8080/v1/lookup-stream` for the same prompt. Expect SSE
output, one `data:` line per chunk, ending in `[DONE]` per the OpenAI SSE
convention. Then `curl http://localhost:8080/v1/lookup` for the same
prompt — confirm the legacy text field still returns the concatenated
answer. The drift test (`go test ./pkg/server -run TestOpenAPIDrift`)
must pass.

**Proxy mode.** `cmd/reverb --proxy openai --upstream
https://api.openai.com --port 8080`. Point a vanilla OpenAI client at
`http://localhost:8080`; issue the same chat completion twice. First
call hits the upstream, second returns from cache (verify by checking
`/v1/stats` after each, or by inspecting the `X-Reverb-Cache: HIT|MISS`
response header). The Ollama path is the offline-CI variant — point
`--upstream` at a local Ollama daemon and assert the same hit pattern.

**Singleflight.** Run `pkg/reverb/singleflight_test.go`'s stress test:
100 concurrent `LookupOrCall`s with a `fill` function that sleeps 50ms
and increments an atomic counter. Assert exactly one `fill` invocation
and 100 successful responses with identical `Response` payloads. Verify
the `reverb_singleflight_coalesced_total` metric incremented by 99.

**PII redaction.** `examples/pii-redaction/` stores a prompt
`"my email is alice@example.com and my phone is 555-123-4567"`. After
store, fetch the entry from the underlying store and assert
`PromptText` reads
`"my email is [EMAIL] and my phone is [PHONE]"`. Lookups for the
non-redacted form must miss; lookups for the redacted form must hit.
Run `go test -bench BenchmarkRedact ./pkg/normalize/redactor/regex` and
assert p99 < 1ms.

**Doc/changelog sweep.** Re-run the forbidden-string check from the
prior spec (manually, until the doc-lint CI step is in place):
`grep -niE 'TODO|not yet wired|known.gap' README.md COMPATIBILITY.md
CHANGELOG.md` should return only the `Known gaps` heading line in
CHANGELOG (which always says `(None)` post-spec).
