# Reverb Compatibility & Release Policy

This document describes what Reverb guarantees across versions: which Go
toolchains are supported, how the public API and wire protocols evolve, and
which transports and backends are stable enough to depend on in production.

- [Release Model](#release-model)
- [Supported Go Versions](#supported-go-versions)
- [API Stability Tiers](#api-stability-tiers)
- [Wire Protocol Compatibility](#wire-protocol-compatibility)
- [Configuration Compatibility](#configuration-compatibility)
- [Transport Stability](#transport-stability)
- [Backend Stability](#backend-stability)
- [Deprecation Policy](#deprecation-policy)
- [Upgrading](#upgrading)

---

## Release Model

Reverb follows [Semantic Versioning 2.0.0](https://semver.org/) once it reaches
`v1.0.0`. Versions are cut as annotated git tags of the form `vMAJOR.MINOR.PATCH`
and published to [pkg.go.dev](https://pkg.go.dev/github.com/nobelk/reverb).

| Component | Meaning after `v1.0.0` |
|---|---|
| **MAJOR** | Incompatible change to a stable API, wire protocol, or config schema. |
| **MINOR** | Backwards-compatible feature additions. New fields, new endpoints, new backends. |
| **PATCH** | Backwards-compatible bug fixes and performance improvements. No API or schema change. |

### Pre-1.0 (current status)

Reverb is currently **pre-1.0**. Per SemVer §4, the following rules apply until
`v1.0.0` is cut:

- Breaking changes may land in `v0.MINOR.0` releases. We will still call them
  out in the [CHANGELOG](CHANGELOG.md) under a **Breaking** heading and in the
  release notes.
- The `v0` line is intended for early adopters. Production users should pin
  exact versions (`go get github.com/nobelk/reverb@v0.X.Y`) rather than version
  ranges.
- The `v1.0.0` release will coincide with the first API surface we are willing
  to support under strict SemVer. Expect that milestone to include: stable
  `pkg/reverb` facade, stable HTTP/gRPC wire protocols, and a stable YAML
  config schema.

## Supported Go Versions

Reverb supports the **two most recent stable Go releases**, matching
[Go's own release policy](https://go.dev/doc/devel/release#policy). `go.mod`
declares the minimum version required to build.

| Reverb version | Minimum Go | Tested on |
|---|---|---|
| `v0.x` (current) | 1.25 | 1.25.x |

When Go N+2 is released, support for Go N is dropped in the next MINOR release.
Dropping a Go version is **not** considered a breaking change under this policy
— it is treated the same as any other toolchain requirement bump.

### What "supported" means

- CI runs the full test suite (unit, integration, conformance) against every
  supported Go version.
- Bug reports filed against older Go versions will be asked to reproduce on a
  supported version.
- We will not add `//go:build go1.N` guards to keep older toolchains working
  once support is dropped.

## API Stability Tiers

Reverb's Go packages fall into three tiers. Import paths in each tier have
different stability guarantees.

### Tier 1 — Stable (public API)

Post-1.0, these packages follow strict SemVer. Breaking changes require a MAJOR
bump.

| Package | Purpose |
|---|---|
| `github.com/nobelk/reverb/pkg/reverb` | Client facade, `Config`, `LookupRequest`, `StoreRequest`, `Response` |
| `github.com/nobelk/reverb/pkg/store` | `Store` interface + `Entry` type |
| `github.com/nobelk/reverb/pkg/vector` | `Index` interface |
| `github.com/nobelk/reverb/pkg/embedding` | `Provider` interface |
| `github.com/nobelk/reverb/pkg/cdc` | `Listener` interface + `Change` event type |

Additions (new methods, new config fields with safe defaults, new optional
interfaces) are MINOR-level changes. Removals, renames, and signature changes
are MAJOR-level.

### Tier 2 — Evolving (backend implementations)

These packages provide concrete implementations of the Tier 1 interfaces.
Their *behavior* under the interface contract is stable, but constructor
signatures and tuning parameters may evolve under MINOR versions pre-1.0.

- `pkg/store/memory`, `pkg/store/redis`, `pkg/store/badger`
- `pkg/vector/flat`, `pkg/vector/hnsw`
- `pkg/embedding/openai`, `pkg/embedding/ollama`, `pkg/embedding/fake`
- `pkg/cdc/webhook`, `pkg/cdc/polling`, `pkg/cdc/nats`
- `pkg/server` (HTTP, gRPC, MCP wrappers)
- `pkg/auth`, `pkg/metrics`, `pkg/normalize`, `pkg/lineage`

Constructor changes will be called out in the CHANGELOG. If a backend is
removed, it will be deprecated for one MINOR cycle first (see [Deprecation
Policy](#deprecation-policy)).

### Tier 3 — Internal (no guarantees)

Anything under `internal/` is private to the module and may change in any
release, including PATCH. Do not import these paths — Go's module system
enforces this at build time.

- `internal/hashutil`
- `internal/retry`
- `internal/testutil`

## Wire Protocol Compatibility

Reverb exposes three wire protocols. Each has its own compatibility story.

### HTTP REST (`/v1/*`)

The `v1` path prefix is a compatibility contract. Within `v1`:

- **Additive changes are backwards-compatible.** New fields may appear in
  request/response bodies. Clients must tolerate unknown fields.
- **Removing or renaming a field is a breaking change** and requires either a
  new path version (`/v2/`) or a MAJOR bump.
- HTTP status codes for documented endpoints will not change meaning within
  `v1`.
- New endpoints may be added under `/v1/` at any MINOR release.

### gRPC (`reverb.v1.ReverbService`)

Defined in `pkg/server/proto/reverb.proto`. The `v1` package name is the
compatibility contract:

- Protocol Buffers' own compatibility rules apply. Adding new fields, new RPCs,
  and new optional message types is backwards-compatible and may land in any
  MINOR release.
- Renaming fields, changing field numbers, changing types, or removing RPCs
  requires a `reverb.v2` package (published alongside `v1` for a deprecation
  window).
- Generated stubs (`reverb.pb.go`, `reverb_grpc.pb.go`) are regenerated from
  the `.proto` as part of the release process. Downstream code-gen from the
  same `.proto` will stay compatible.

### MCP (JSON-RPC)

The MCP wrapper (`pkg/server/mcp`) is **experimental** as of `v0.x`. The
tool surface, tool names, and argument schemas may change in any MINOR
release without a deprecation window. It will be promoted to stable once the
surface stabilizes and the MCP ecosystem reaches a comparable bar.

## Configuration Compatibility

The YAML config schema (`pkg/reverb/config.go` and `cmd/reverb` flags) follows
the same tiering as the Go API:

- **Adding a field with a safe default** is backwards-compatible.
- **Removing or renaming a field** is a breaking change. Removals go through
  one deprecation MINOR cycle where the old name continues to work with a
  logged warning.
- **Changing the default value of an existing field** is a breaking change if
  it alters observable behavior. Defaults may only change in MAJOR releases
  post-1.0, or in a clearly-documented MINOR release pre-1.0.

Environment variable overrides (`REVERB_*`) follow the same rules.

### CLI flags

`cmd/reverb` CLI flags (e.g. `--http-addr`, `--config`) are covered by this
policy as if they were config fields.

## Transport Stability

| Transport | Status | Notes |
|---|---|---|
| Embedded library (`pkg/reverb`) | **Stable** | Primary supported integration mode. |
| HTTP REST (`/v1/*`) | **Stable** | Default container entrypoint. Covered by integration tests. |
| gRPC (`reverb.v1.ReverbService`) | **Stable** | Same `Client` instance as HTTP; both tested. |
| MCP (`pkg/server/mcp`) | **Experimental** | Added in `v0.1.0-dev`. Surface may change without deprecation. |

## Backend Stability

### Stores

| Backend | Status | Recommended for |
|---|---|---|
| `memory` | **Stable** | Dev, tests, single-replica deployments with no durability need. |
| `redis` | **Stable** | Shared state across replicas. Production default. |
| `badger` | **Stable** | Single-node durable deployments. Embedded use cases. |

All stores must pass the `pkg/store/conformance` suite before release.

### Vector Indices

| Backend | Status | Recommended for |
|---|---|---|
| `flat` | **Stable** | Up to ~50K entries per namespace. Exact brute-force search. |
| `hnsw` | **Stable** | Up to ~10M entries per namespace. Approximate O(log n) search. |

All indices must pass the `pkg/vector/conformance` suite before release. HNSW
tuning parameters (`hnsw_m`, `hnsw_ef_construction`, `hnsw_ef_search`) are
Tier 2 and may be retuned in MINOR releases if we find better defaults.

### Embedding Providers

| Backend | Status | Recommended for |
|---|---|---|
| `openai` | **Stable** | Production. Matches OpenAI embedding API. |
| `ollama` | **Stable** | Self-hosted embeddings. |
| `fake` | **Test-only** | Deterministic hash-based embedder. Not for production. |

### CDC Listeners

| Backend | Status | Recommended for |
|---|---|---|
| `webhook` | **Stable** | Pushed change notifications from CMS/wiki/knowledge sources. |
| `polling` | **Stable** | Library-mode only (not wired into standalone binary). |
| `nats` | **Stable** | JetStream-based event streams. |

### Auxiliary

| Component | Status | Notes |
|---|---|---|
| Auth (`pkg/auth`) | **Beta** | Bearer token (HTTP) and API key (gRPC). Multi-tenant scoping settled but may gain more options. |
| Metrics (`pkg/metrics`) | **Beta** | Prometheus + OTel tracing wired. Metrics HTTP server in the standalone binary is not yet started — tracked as a known gap. |

## Deprecation Policy

When we remove a stable API, config field, backend, or endpoint:

1. **Announce** in the CHANGELOG under `Deprecated` with the targeted removal
   version and a migration path.
2. **Keep it working** for at least one MINOR release, logging a deprecation
   warning at first use (where practical).
3. **Remove** in the announced MINOR (pre-1.0) or MAJOR (post-1.0) release,
   listed under `Removed` in the CHANGELOG.

Pre-1.0, the deprecation window may be shorter (down to one MINOR) if no users
have adopted the feature. This will always be explicit in the CHANGELOG entry.

## Upgrading

### What each version bump means for you

| Bump | Action required |
|---|---|
| **PATCH** (`v0.1.0` → `v0.1.1`) | Drop-in. Safe to apply without review. |
| **MINOR** post-1.0 (`v1.2.0` → `v1.3.0`) | Drop-in. Read CHANGELOG for new features. |
| **MINOR** pre-1.0 (`v0.1.0` → `v0.2.0`) | Read CHANGELOG's **Breaking** section. May require code or config changes. |
| **MAJOR** post-1.0 (`v1.x` → `v2.0.0`) | Migration required. Release notes will include a migration guide. |

### Upgrade testing checklist

Before rolling a new Reverb version to production, we recommend:

1. Diff the [CHANGELOG](CHANGELOG.md) from your current version to the target.
2. Re-run your config through `cmd/reverb --config ... --validate` (planned —
   until then, start the server in a staging environment and confirm it boots).
3. Replay a sample of production traffic through a staging replica and confirm
   hit rates and latency are unchanged. Reverb's cache semantics are behavioral
   as well as structural — a change in normalization or similarity threshold
   defaults would show up as a hit-rate regression, not a compile error.
4. Validate that any backend you depend on (Redis, Badger, NATS) is still
   supported at its pinned version. See the `go.mod` `require` block for the
   tested versions.

### Rollback

Rollback is always safe **within a MAJOR line** — older cache entries written
by newer versions are designed to be readable by the previous MINOR. Across
MAJOR versions, consult the release notes for a rollback window (may require
a cold cache).

---

Questions or concerns about the policy? Open an issue at
[github.com/nobelk/reverb/issues](https://github.com/nobelk/reverb/issues).
