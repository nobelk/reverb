# Code Review Task

Review ALL of the following newly implemented Go files in /Users/nobelk/sources/reverb for bugs, errors, race conditions, and design issues. This is a semantic response cache library.

## Files to Review (18 files)

### Storage backends:
- pkg/store/badger/badger.go + badger_test.go (BadgerDB persistent store)
- pkg/store/redis/redis.go + redis_test.go (Redis store)

### Embedding/CDC:
- pkg/embedding/ollama/ollama.go + ollama_test.go (Ollama embedding provider)
- pkg/cdc/nats/nats.go + nats_test.go (NATS CDC listener)

### Server:
- pkg/server/grpc.go + grpc_test.go (gRPC server)
- pkg/server/http.go (updated — stats hit_rate)

### Core client:
- pkg/reverb/client.go (updated — background goroutines, metrics wiring)
- pkg/reverb/options.go (new — functional options)

### Observability:
- pkg/metrics/metrics.go + metrics_test.go (Prometheus metrics)
- pkg/metrics/tracing.go + tracing_test.go (OpenTelemetry tracing)

### Config:
- cmd/reverb/main.go (updated — YAML config, factories)

## What to look for:

1. **Bugs**: nil pointer dereferences, incorrect error handling, missing returns, logic errors
2. **Race conditions**: unprotected shared state, goroutine leaks, missing mutex/atomic usage
3. **Resource leaks**: unclosed connections, channels, file handles, goroutines not stopped on Close()
4. **Interface compliance**: do implementations fully satisfy their interfaces?
5. **Error handling**: swallowed errors, missing context wrapping, panics
6. **Test gaps**: untested error paths, missing edge cases
7. **Design issues**: violations of the store.Store / embedding.Provider / cdc.Listener contracts

## Output format:

For each finding, report:
```
[SEVERITY: CRITICAL|HIGH|MEDIUM|LOW] file:line_range
Description of the issue
Suggested fix
```

Write your full review to: /Users/nobelk/sources/reverb/.omc/research/code-review.md
