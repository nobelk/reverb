# Reverb

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![Build Status](https://img.shields.io/github/actions/workflow/status/intuitai/reverb/ci.yml?branch=main&label=build)](https://github.com/intuitai/reverb/actions)
[![Coverage](https://img.shields.io/codecov/c/github/intuitai/reverb?label=coverage)](https://codecov.io/gh/intuitai/reverb)

**Semantic Response Cache with Knowledge-Aware Invalidation**

Reverb is a Go library and standalone HTTP service that provides a two-tier semantic response cache for LLM-powered applications. It reduces redundant LLM calls by caching both exact and semantically similar queries, and automatically invalidates cached entries when the underlying knowledge base changes.

```
Application
    │
    ▼
reverb.Lookup(req)
    │
    ├── Tier 1: Exact Match ◄── SHA-256 of normalized prompt
    │       miss
    ├── Tier 2: Semantic    ◄── embedding cosine similarity
    │       miss
    ▼
Call LLM (your code)
    │
    ▼
reverb.Store(req, resp, sources)
```

## Features

- **Two-tier cache** — Exact match (SHA-256 hash, sub-millisecond) and semantic similarity (embedding cosine, ~50ms)
- **Knowledge-aware invalidation** — Tracks which source documents contributed to each cached response; automatically invalidates when sources change
- **CDC listeners** — Webhook, polling, and NATS-based change-data-capture for source document updates
- **Namespace isolation** — Logical partitions for multi-tenant or multi-use-case deployments
- **Pluggable backends** — Interfaces for embedding providers, vector indices, and persistence stores (memory, Redis, BadgerDB)
- **Standalone HTTP & gRPC servers** — REST and gRPC APIs for language-agnostic integration
- **Minimal dependencies** — No infrastructure required; core library runs with in-memory store and flat vector index

## Quick Start

### As a Go Library

```go
package main

import (
    "context"
    "fmt"

    "github.com/org/reverb/pkg/embedding/fake"
    "github.com/org/reverb/pkg/reverb"
    "github.com/org/reverb/pkg/store/memory"
    "github.com/org/reverb/pkg/vector/flat"
)

func main() {
    ctx := context.Background()

    client, _ := reverb.New(
        reverb.Config{
            DefaultNamespace:    "support-bot",
            SimilarityThreshold: 0.95,
        },
        fake.New(64),    // embedding provider (use openai.New in production)
        memory.New(),    // persistence store
        flat.New(),      // vector index
    )
    defer client.Close()

    // Store a response
    client.Store(ctx, reverb.StoreRequest{
        Namespace: "support-bot",
        Prompt:    "How do I reset my password?",
        ModelID:   "gpt-4o",
        Response:  "Go to Settings > Security > Reset Password.",
    })

    // Lookup — returns exact hit
    resp, _ := client.Lookup(ctx, reverb.LookupRequest{
        Namespace: "support-bot",
        Prompt:    "How do I reset my password?",
        ModelID:   "gpt-4o",
    })
    fmt.Printf("Hit: %v, Tier: %s\n", resp.Hit, resp.Tier)
    // Output: Hit: true, Tier: exact
}
```

### As a Standalone Server

```bash
# Build and run
make build
./bin/reverb --http-addr :8080

# Or with Docker
docker build -t reverb:latest .
docker run -p 8080:8080 reverb:latest
```

## HTTP API

| Method | Endpoint | Description |
|---|---|---|
| `POST` | `/v1/lookup` | Check cache for a matching response |
| `POST` | `/v1/store` | Store a new cache entry |
| `POST` | `/v1/invalidate` | Invalidate entries by source ID |
| `DELETE` | `/v1/entries/{id}` | Delete a single cache entry |
| `GET` | `/v1/stats` | Cache statistics |
| `GET` | `/healthz` | Health check |

### Example: Store and Lookup

```bash
# Store
curl -X POST http://localhost:8080/v1/store \
  -H 'Content-Type: application/json' \
  -d '{
    "namespace": "support-bot",
    "prompt": "How do I reset my password?",
    "model_id": "gpt-4o",
    "response": "Go to Settings > Security > Reset Password.",
    "sources": [{"source_id": "doc:password-guide", "content_hash": "a1b2c3..."}],
    "ttl_seconds": 86400
  }'

# Lookup
curl -X POST http://localhost:8080/v1/lookup \
  -H 'Content-Type: application/json' \
  -d '{
    "namespace": "support-bot",
    "prompt": "How do I reset my password?",
    "model_id": "gpt-4o"
  }'

# Invalidate when source changes
curl -X POST http://localhost:8080/v1/invalidate \
  -H 'Content-Type: application/json' \
  -d '{"source_id": "doc:password-guide"}'
```

## gRPC API

The `reverb.v1.ReverbService` exposes the same operations over gRPC (see `pkg/server/proto/reverb.proto`):

| RPC | Description |
|---|---|
| `Lookup` | Check cache for a matching response |
| `Store` | Store a new cache entry |
| `Invalidate` | Invalidate entries by source ID |
| `DeleteEntry` | Delete a single cache entry |
| `GetStats` | Cache statistics |

## Architecture

### Package Structure

```
reverb/
├── cmd/reverb/              # Standalone server binary
├── pkg/
│   ├── reverb/              # Public API — Client facade, Config
│   ├── cache/
│   │   ├── exact/           # Tier 1: SHA-256 exact-match cache
│   │   └── semantic/        # Tier 2: embedding similarity cache
│   ├── embedding/           # Embedding provider interface
│   │   ├── fake/            # Deterministic fake for tests
│   │   ├── openai/          # OpenAI API implementation
│   │   └── ollama/          # Ollama local embeddings
│   ├── normalize/           # Prompt normalization (NFC, lowercase, whitespace)
│   ├── lineage/             # Source lineage tracking + invalidation engine
│   ├── cdc/                 # Change-data-capture listener interface
│   │   ├── webhook/         # HTTP webhook CDC listener
│   │   ├── polling/         # Polling-based CDC listener
│   │   └── nats/            # NATS JetStream CDC listener
│   ├── store/               # Persistence store interface
│   │   ├── memory/          # In-memory store (dev/test)
│   │   ├── redis/           # Redis-backed store
│   │   ├── badger/          # BadgerDB embedded store
│   │   └── conformance/     # Shared conformance test suite
│   ├── vector/              # Vector index interface
│   │   ├── flat/            # Brute-force O(n) index (up to ~50K entries)
│   │   ├── hnsw/            # HNSW O(log n) index (up to ~10M entries)
│   │   └── conformance/     # Shared conformance test suite
│   ├── server/              # HTTP REST and gRPC API servers
│   │   └── proto/           # Protobuf service definitions
│   └── metrics/             # Metrics collector + tracing stubs
├── internal/
│   ├── hashutil/            # SHA-256 hashing helpers
│   ├── retry/               # Exponential backoff with jitter
│   └── testutil/            # FakeClock, EntryBuilder, test helpers
├── Dockerfile               # Production multi-stage build
├── Dockerfile.test          # Test runner image
└── test/
    ├── integration/         # End-to-end HTTP integration tests
    └── docker-compose.yml   # Container test infrastructure
```

### Core Interfaces

All backends are pluggable via interfaces:

- **`embedding.Provider`** — Generate embedding vectors from text (OpenAI, Ollama, fake)
- **`vector.Index`** — Approximate nearest neighbor search (flat, HNSW)
- **`store.Store`** — Durable persistence for cache entries (memory, Redis, BadgerDB)
- **`cdc.Listener`** — Watch for source document changes (webhook, polling, NATS)

### Lookup Flow

1. **Normalize** prompt (NFC, lowercase, collapse whitespace, trim, strip trailing punctuation)
2. **Exact tier** — SHA-256 hash of (namespace + prompt + model_id), lookup in store
3. **Semantic tier** — Compute embedding, search vector index for top-k candidates above threshold
4. **Miss** — Return miss; caller invokes LLM and calls `Store()`

### Invalidation Flow

1. CDC listener detects source document change (or deletion)
2. Lineage index maps source ID to affected cache entry IDs
3. If source deleted (zero hash): invalidate all dependent entries
4. Otherwise: compare stored content hash against new hash; if changed, delete entries from both store and vector index

## Build

**Requirements:** Go 1.25+

```bash
# Build the binary
make build

# Build the Docker image
make docker
```

## Testing

```bash
# Unit tests with race detection
make test

# Same as above (explicit name)
make test-unit

# Integration tests (requires Docker — starts a Reverb server container)
make test-integration

# Full containerized test pipeline (unit + integration, zero host deps beyond Docker)
make test-all

# Test coverage report
make coverage

# Benchmarks
make bench
```

### Test Summary

| Category | Count | Description |
|---|---|---|
| Unit tests | 239 | All packages, race-free |
| Integration tests | 11 | Full HTTP API end-to-end |
| Conformance suites | 2 | Store + VectorIndex shared suites |
| Test packages | 19 | With tests |

## Configuration

```go
reverb.Config{
    DefaultNamespace:    "default",         // logical partition
    DefaultTTL:          24 * time.Hour,    // cache entry time-to-live
    SimilarityThreshold: 0.95,              // cosine similarity for semantic hits
    SemanticTopK:        5,                 // max candidates from vector search
    ScopeByModel:        true,              // scope semantic search by model ID
}
```

- **`ScopeByModel`** — When `true`, semantic search results are filtered by model ID so queries for `gpt-4o` cannot match entries stored for a different model.
- **TTL** — In the Go API, use `TTL time.Duration` on `StoreRequest`. The HTTP API accepts `ttl_seconds` as an integer.

## Dependencies

| Dependency | Purpose |
|---|---|
| `golang.org/x/text` | Unicode NFC normalization |
| `github.com/google/uuid` | UUID generation for entry IDs |
| `github.com/redis/go-redis/v9` | Redis store backend |
| `github.com/dgraph-io/badger/v4` | BadgerDB embedded store backend |
| `github.com/nats-io/nats.go` | NATS JetStream CDC listener |
| `google.golang.org/grpc` | gRPC server transport |
| `google.golang.org/protobuf` | Protocol Buffers serialization |
| `github.com/prometheus/client_golang` | Prometheus metrics |
| `go.opentelemetry.io/otel` | OpenTelemetry tracing |
| `github.com/stretchr/testify` | Test assertions (dev only) |

## License

See [LICENSE](LICENSE) for details.
