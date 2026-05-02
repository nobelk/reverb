# Tech Stack

This document records the load-bearing technical commitments. It distinguishes **constitutional** choices (rejected only by amending this file) from **incidental** choices (current implementations that can be swapped).

For the live dependency list, read `go.mod`; this file describes the policies that govern it.

## Language and runtime

| Concern | Choice | Status | Notes |
|---|---|---|---|
| Implementation language | Go | **Constitutional** | Load-bearing for performance and single-binary deploy. Migrating off Go is explicitly out of scope per `mission.md`. |
| Repository composition | Go-only main repo | **Constitutional** | The main `nobelk/reverb` repository contains only Go source. Non-Go toolchains (JS/TS for the admin UI in `roadmap.md` 2.24, Python/TS SDKs in 1.2/1.3, dashboards JSON, etc.) live in sibling repos so the main repo stays buildable with `go build ./...` alone. |
| Minimum Go version | 1.25 | Incidental | Floor moves forward with Go releases; the project tracks the two most recent stable Go versions. |
| CGO | Not required in the core | **Constitutional** | The core library and standalone binary build with `CGO_ENABLED=0`. CGO-dependent features (e.g., ONNX-runtime re-rankers) ship behind a build tag and are opt-in. |
| Concurrency model | Standard `context.Context` propagation, goroutines, channels | Constitutional | All public APIs accept `context.Context` as the first argument. Cancellation must be honored. |

## Public surface

| Surface | Contract | Status |
|---|---|---|
| Go library | `pkg/reverb` and other packages under `pkg/` | **Constitutional** — semver-governed; breaking changes require a major version. |
| HTTP REST | Versioned under `/v1/`. `openapi/v1.yaml` is the authoritative source of truth (rendered at https://nobelk.github.io/reverb/). The drift-check test in `pkg/server/openapi_drift_test.go` enforces handler↔spec alignment on every build. | **Constitutional** — the `/v1/` path prefix, additive-only evolution rules, and the OpenAPI artifact's authoritative status all apply. |
| gRPC | `pkg/server/proto/reverb.proto`, service `reverb.v1.ReverbService` | **Constitutional** — proto evolution rules apply (no field renumbering, no removed fields within a major). |
| MCP JSON-RPC | `pkg/server/mcp` | Beta-track; not yet under the same stability promise. Graduation criteria are listed explicitly in `roadmap.md` item 2.21. |
| `internal/` packages | Private | No stability promise. Used precisely so the core can keep moving. |

## Pluggable interfaces

The interfaces below are the project's extension surface. Each must ship with at least one zero-dependency reference implementation and at least one production-grade implementation, per principle 3 in `mission.md`.

| Interface | Zero-dep reference | Production impls (current) |
|---|---|---|
| `embedding.Provider` | `pkg/embedding/fake` | `pkg/embedding/openai`, `pkg/embedding/ollama` |
| `vector.Index` | `pkg/vector/flat` | `pkg/vector/hnsw` |
| `store.Store` | `pkg/store/memory` | `pkg/store/redis`, `pkg/store/badger` |
| `cdc.Listener` | `pkg/cdc/polling` (library mode) | `pkg/cdc/webhook`, `pkg/cdc/nats` |

New backends must pass the shared conformance suites under `pkg/store/conformance` and `pkg/vector/conformance` before merge.

## Dependency policy

The bar for a *required* runtime dependency is high; the bar for an opt-in dependency in a backend package is low.

- **Required (core library path — `pkg/...` excluding backend packages):** Only standard library plus a minimal set of widely trusted modules — Unicode handling (`golang.org/x/text`), UUID generation (`github.com/google/uuid`), and the gRPC + protobuf runtime (`google.golang.org/grpc`, `google.golang.org/protobuf`). Adding to this set is a constitutional change and requires amending this file.
- **Required (standalone server only — `cmd/reverb`):** YAML config parsing (`gopkg.in/yaml.v3`), Prometheus client (`prometheus/client_golang`), and OpenTelemetry (`go.opentelemetry.io/otel`). These are required when running the binary but are wired so library consumers (`pkg/reverb` users) do not depend on them transitively. Adding to this set is also a constitutional change.
- **Opt-in (backend packages):** Each backend may pull in its own client library (e.g., `redis/go-redis/v9`, `dgraph-io/badger/v4`, `nats-io/nats.go`). Users only pay the cost if they import that backend.
- **Test-only:** Test assertions (`stretchr/testify`) and test infrastructure live in test files; they do not appear in the dependency graph of the published binary.

## Tooling

| Concern | Choice | Status |
|---|---|---|
| Build | `make` targets in `Makefile` | Incidental |
| Container | Multi-stage `Dockerfile`, separate `Dockerfile.test` | Incidental |
| CI | GitHub Actions (`.github/workflows`) | Incidental |
| Coverage | Codecov | Incidental |
| Benchmarking | `go test -bench`, results published in `BENCHMARKS.md` | Constitutional intent — published reference baselines and false-positive budgets are not optional. |

## Configuration

- The standalone server reads YAML via `--config` and supports environment-variable overrides for sensitive values (API keys, passwords, OTel endpoints). The full schema lives in `pkg/reverb/config.go`.
- The library accepts a `reverb.Config` struct passed to `reverb.New`. There is no global state.
- New configuration keys are additive; removing or renaming a key follows the same semver rules as the public API.

## Deployment shapes

Reverb is delivered three ways. The library is the primary product; the others are derived.

1. **Go library import** (`go get github.com/nobelk/reverb`) — the canonical integration. Quick Start runs with zero external services.
2. **Standalone binary** (`./bin/reverb` or the published Docker image) — for non-Go consumers and shared-service deployments. Configured via YAML and env vars; speaks HTTP + gRPC over the same core.
3. **Language SDKs** (planned: Python, TypeScript; later: Java, Rust) — generated from the published OpenAPI 3.1 spec and `.proto` files. SDKs add only thin language-idiomatic sugar over the wire contract; they do not introduce features the underlying API lacks.

There is no managed cloud offering and there will not be one (per `mission.md`).

## Quality and correctness commitments

These are constitutional and tracked in CI:

- A published false-positive budget (currently 0/10 `UnrelatedPairs` at threshold 0.95) is enforced on every build. It tightens, never loosens, across releases.
- Conformance test suites under `pkg/store/conformance` and `pkg/vector/conformance` are the gating contract for new backends.
- Latency reference numbers in `BENCHMARKS.md` are reproducible and gate regressions.
- All exported symbols in public packages carry godoc; this is part of the definition of done.

## License

Apache License 2.0. The license is constitutional and changing it requires explicit consent from all contributors.

## Amendment process

Changing any row marked **Constitutional** above requires:

1. An explicit amendment to this file in the same pull request that introduces the change.
2. A reference to the amendment in the pull request description.
3. Reviewer sign-off that explicitly acknowledges the constitutional change.

Incidental rows can be updated with the same scrutiny as ordinary code changes.
