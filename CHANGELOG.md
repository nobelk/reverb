# Changelog

All notable changes to Reverb will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and Reverb adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in [COMPATIBILITY.md](COMPATIBILITY.md).

Section conventions:

- **Added** — new features, endpoints, backends.
- **Changed** — non-breaking changes to existing behavior.
- **Breaking** — incompatible changes. Pre-1.0, these may appear in MINOR
  releases; post-1.0, only in MAJOR releases.
- **Deprecated** — still-working features slated for removal. Includes the
  target removal version and a migration path.
- **Removed** — features that were previously deprecated and are now gone.
- **Fixed** — bug fixes.
- **Security** — vulnerability fixes.

---

## [Unreleased]

_No changes yet._

---

## [0.1.0] — 2026-04-21

Initial tagged release. Establishes the public Go API, wire protocols, and
backend surface that Reverb is willing to support under the
[compatibility policy](COMPATIBILITY.md).

### Added

- MCP (Model Context Protocol) JSON-RPC wrapper at `pkg/server/mcp`. Marked
  **experimental** per [COMPATIBILITY.md](COMPATIBILITY.md#transport-stability)
  — tool surface may change without a deprecation window.
- Auth and multi-tenant scoping (`pkg/auth`) with bearer token on HTTP and
  API-key metadata on gRPC. Marked **beta**.
- OpenTelemetry tracing and Prometheus metrics (`pkg/metrics`).
- Benchmark suite under `benchmark/`.
- Explicit embedding dimensionality validation at `Client` construction.
- Production runbook ([`RUNBOOK.md`](RUNBOOK.md)).
- Compatibility and release policy ([`COMPATIBILITY.md`](COMPATIBILITY.md))
  and this changelog.

### Public Go API

- `pkg/reverb` — `Client`, `Config`, `LookupRequest`, `StoreRequest`, `Response`.
- `pkg/store` — `Store` interface with `memory`, `redis`, and `badger`
  implementations. Shared conformance suite at `pkg/store/conformance`.
- `pkg/vector` — `Index` interface with `flat` (brute-force) and `hnsw`
  implementations. Shared conformance suite at `pkg/vector/conformance`.
- `pkg/embedding` — `Provider` interface with `openai`, `ollama`, and `fake`
  implementations.
- `pkg/cdc` — `Listener` interface with `webhook`, `polling`, and `nats`
  implementations.

### Transports

- Embedded library (`pkg/reverb`).
- HTTP REST at `/v1/*` (`pkg/server` HTTP server).
- gRPC `reverb.v1.ReverbService` (`pkg/server` gRPC server, proto at
  `pkg/server/proto/reverb.proto`).
- MCP JSON-RPC (experimental, `pkg/server/mcp`).

### Standalone binary

- `cmd/reverb` with YAML config via `--config`, flag overrides, and
  `REVERB_*` environment variable overrides for secrets.
- Multi-stage production Dockerfile and test-runner Dockerfile.

### Toolchain

- Go 1.25+.

### Known gaps

- The `metrics` HTTP server is not yet started by `cmd/reverb`, though
  port `9100` is exposed in the container image and the `metrics` config
  section is parsed.
- The `--validate` flag referenced in [COMPATIBILITY.md](COMPATIBILITY.md#upgrade-testing-checklist)
  is not yet implemented.

---

[Unreleased]: https://github.com/nobelk/reverb/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/nobelk/reverb/releases/tag/v0.1.0
