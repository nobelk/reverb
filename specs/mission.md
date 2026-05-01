# Mission

## Vision

Reverb is the semantic response cache that LLM apps can trust. Where most caches optimize for hit-rate at the cost of staleness, Reverb is built around a single hard commitment: **a hit must be a hit**. When the underlying knowledge changes, the cache notices and gets out of the way.

Reverb is delivered first as an idiomatic Go library, with a standalone HTTP/gRPC server and language SDKs as secondary surfaces over the same core.

## Audience

The primary audience is **Go developers building LLM-powered applications** — RAG pipelines, support and product chatbots, agentic systems, internal copilots. They are comfortable wiring backends together, allergic to heavy frameworks, and want a cache they can drop into a service in five lines and reason about end-to-end.

Secondary audiences inherit the library, not lead it:

- **Non-Go application developers** consuming Reverb via Python/TypeScript SDKs generated from the same OpenAPI/proto contract.
- **Platform and SRE teams** running the standalone server as shared infrastructure for many internal LLM apps.

The constitution optimizes for the primary audience. When a decision pulls between library ergonomics for Go developers and convenience for the secondary audiences, library ergonomics wins.

## Scope

### In scope

- Two-tier response cache: SHA-256 exact match plus embedding-similarity semantic match.
- Source-lineage tracking and CDC-driven invalidation when source documents change.
- Pluggable interfaces for embedding providers, vector indices, persistence stores, and CDC listeners — each with at least one zero-dependency and one production-grade implementation.
- A Go library (`pkg/reverb`) and a standalone HTTP + gRPC server (`cmd/reverb`) sharing the same core engine.
- Cross-language consumption via SDKs and adapters (Python, TypeScript, framework integrations) generated from a published OpenAPI 3.1 spec and the existing `.proto` contract.
- Quality controls: enforced false-positive budgets, conformance test suites for backends, published latency baselines.
- An optional `LookupOrFetch`-style gateway mode that calls the host's LLM provider on miss. Strictly opt-in; the lookup-only path remains the primary, recommended integration shape.

### Out of scope

These are not subjects for debate; rejecting them is the point of the constitution.

- **Hosted SaaS operated by the maintainers.** Reverb is self-host-first, forever. There is no managed control plane, no billing, no signup. Users deploy the library or the binary themselves.
- **A general-purpose vector database.** The vector index serves the cache only. No filtering DSLs, no multi-vector queries, no positioning against pgvector, Qdrant, or Pinecone — those are backends Reverb plugs into, not products it competes with.
- **A built-in LLM gateway as the primary surface.** Routing, fallback, model selection, and provider arbitrage belong in tools like LiteLLM, Portkey, and OpenRouter. The optional `LookupOrFetch` mode is a thin convenience helper, not a gateway product.
- **Custom embedding model training, fine-tuning, or distillation.** Reverb consumes embeddings via the `Provider` interface and ships nothing of its own beyond a deterministic test fake.
- **A GraphQL transport.** Three transports (Go, HTTP, gRPC) is already a maintenance tax.
- **Migrating off Go.** The Go choice is load-bearing for performance and single-binary deploy and is not relitigated.

## Guiding principles

These four principles are non-negotiable. Pull requests are measured against them; conflicts between them are resolved in the order listed below.

### 1. Correctness over hit-rate

A cache that returns wrong answers is worse than no cache. Reverb publishes a false-positive budget and enforces it in CI; that budget is a constitutional commitment, not a benchmark. Features that would raise the hit-rate at the cost of correctness — looser default thresholds, opportunistic match-shaping, ignoring lineage signals — are rejected. When tuning conflicts with correctness, correctness wins.

**This means we say no to:** lowering the default similarity threshold to win benchmarks; serving entries whose lineage is known stale; "best-effort" invalidation paths that can drop CDC events silently.

### 2. Zero required dependencies for Quick Start

The library must always run with `memory.New()` + `flat.New()` + `fake.New(N)` — no Redis, no Postgres, no API keys, no Docker. Adding a *required* runtime dependency to the core path is a constitutional violation. New backends are opt-in and live behind their own packages.

**This means we say no to:** mandatory metrics endpoints in the core library; required CGO; required network calls for first-run examples; making any one shipped backend "the" backend.

### 3. Pluggable everything, ship one of each

Every backend is an interface, and every interface ships with at least two implementations: one zero-dependency reference (in-memory, flat, fake) and at least one production-grade implementation. New extension points are introduced as interfaces *first*, not as concrete types. This prevents lock-in to any single infrastructure choice and keeps the project honest about what it is — orchestration, not infrastructure.

**This means we say no to:** features that only work with a specific store/index/embedder; "we'll add the interface later" PRs; helper functions that bypass the interface for a hot path.

### 4. Library API stability is sacred

The public surface of `pkg/reverb` (and any other package marked as public) follows strict semver. Breaking changes require a major version. godoc completeness — every exported symbol has a doc comment, examples for non-obvious flows — is part of the definition-of-done for any new public API. Internal packages under `internal/` carry no such promise and exist precisely so the core can keep moving.

**This means we say no to:** silently changing public type shapes; renaming exported symbols outside a major version bump; merging public APIs without godoc; using `internal/` to hide breaking changes that should have been a major bump.

## How this document is used

- New feature proposals open with a one-line argument from these principles. If the argument requires bending one of them, that is a constitutional change and needs explicit amendment of this file, not an exception.
- Pull request reviewers can reject changes citing these sections by number.
- When `mission.md`, `tech-stack.md`, and `roadmap.md` disagree, this file wins.
