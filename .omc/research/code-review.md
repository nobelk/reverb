# Reverb Semantic Cache Library -- Full Code Review

**Reviewer:** code-reviewer (opus)
**Date:** 2026-04-03
**Branch:** feature-complete
**Files Reviewed:** 18
**Total Issues:** 22

---

## Severity Summary

| Severity | Count | Action Required |
|----------|-------|-----------------|
| CRITICAL | 2     | Must fix before merge |
| HIGH     | 6     | Should fix before merge |
| MEDIUM   | 8     | Consider fixing |
| LOW      | 6     | Optional improvements |

---

## CRITICAL Issues

### C1. [CRITICAL] Race condition: Redis `IncrementHit` is not atomic (read-modify-write without transaction)

**File:** `/Users/nobelk/sources/reverb/pkg/store/redis/redis.go:174-192`

The `IncrementHit` method performs a non-atomic read-modify-write cycle: it calls `Get` (line 178), increments `HitCount` in Go memory (line 185), then writes back with `HSet` (line 191). Under concurrent load, two goroutines can read the same `HitCount`, both increment to the same value, and one increment is silently lost.

This is especially likely because `client.go:317-322` and `client.go:341-346` fire `IncrementHit` from background goroutines on every cache hit.

**Fix:** Use a Redis Lua script or `WATCH`/`MULTI`/`EXEC` optimistic locking to make the increment atomic:
```go
func (s *Store) IncrementHit(ctx context.Context, id string) error {
    script := redis.NewScript(`
        local data = redis.call('HGET', KEYS[1], 'data')
        if not data then return 0 end
        local entry = cjson.decode(data)
        entry.HitCount = (entry.HitCount or 0) + 1
        entry.LastHitAt = ARGV[1]
        redis.call('HSET', KEYS[1], 'data', cjson.encode(entry))
        return 1
    `)
    return script.Run(ctx, s.client, []string{s.entryKey(id)}, time.Now().Format(time.RFC3339Nano)).Err()
}
```
Alternatively, store `HitCount` as a separate Redis field and use `HINCRBY`.

---

### C2. [CRITICAL] Race condition: Redis `Put` has a TOCTOU gap between read-old and write-new

**File:** `/Users/nobelk/sources/reverb/pkg/store/redis/redis.go:77-118`

`Put` reads the old entry via `s.Get()` (line 83) outside any transaction, then conditionally cleans up old indices (lines 88-99), then writes new data in a pipeline (lines 107-113). Between the `Get` and the `Pipeline.Exec`, another goroutine could modify or delete the entry, causing:
- Stale hash indices left behind (old index never cleaned)
- Lineage indices becoming inconsistent

The Badger implementation correctly handles this inside a single `db.Update` transaction (line 140).

**Fix:** Wrap the entire read-old + cleanup + write-new sequence in a Redis transaction using `WATCH` on the entry key with `TxPipeline`, or use a Lua script to perform the entire operation atomically.

---

## HIGH Issues

### H1. [HIGH] Duplicate CDC invalidation pipeline in `main.go` -- events processed twice

**File:** `/Users/nobelk/sources/reverb/cmd/reverb/main.go:87-125`

`reverb.New()` at line 87 is called **without** `WithCDCListener(listener)`, so the client's internal `invalidationLoop` goroutine (client.go:133-135) starts with an empty `eventCh` that nothing writes to. Then main.go manually creates a **second** CDC listener + event channel (lines 105-122) and calls `client.Invalidate()` directly.

If someone later adds `WithCDCListener(listener)` to the `reverb.New()` call (which is the designed pattern per options.go:29-33), events would be double-processed: once by the internal `invalidationLoop` and once by main.go's manual goroutine.

More immediately, the internal `invalidationLoop` goroutine is running but starved (its channel is never written to), which is wasted resources. The manual goroutine at lines 111-122 also has a goroutine leak: when the `events` channel is closed (after the listener exits), the `for/select` loop will busy-spin receiving zero-value events from the closed channel, because `case ev := <-events` returns immediately with a zero value on a closed channel and there is no `ok` check.

**Fix:** Either:
1. Pass the listener via `WithCDCListener(listener)` and remove the manual goroutine, OR
2. Remove the internal `invalidationLoop` startup from `New()` when no listener is provided.

Also fix the goroutine leak by checking the channel-closed sentinel:
```go
case ev, ok := <-events:
    if !ok {
        return
    }
```

---

### H2. [HIGH] `invalidationLoop` uses `context.Background()` instead of `c.ctx` for ProcessEvent

**File:** `/Users/nobelk/sources/reverb/pkg/reverb/client.go:178`

Inside the flush function, `c.invalidator.ProcessEvent(context.Background(), ...)` is called with `context.Background()`. This means that even after the client is closed (cancelling `c.ctx`), invalidation operations will continue executing with an uncancellable context. If the store is slow or the network is down, this can block `Close()` indefinitely because `c.wg.Wait()` (line 481) waits for the `invalidationLoop` to finish, but `ProcessEvent` with `context.Background()` never times out.

**Fix:** Use `c.ctx` (or a derived context with a timeout) instead of `context.Background()`:
```go
n, err := c.invalidator.ProcessEvent(c.ctx, lineageEv)
```

---

### H3. [HIGH] Redis error comparison uses `==` instead of `errors.Is`

**File:** `/Users/nobelk/sources/reverb/pkg/store/redis/redis.go:51,69,163,211,251`

All five `goredis.Nil` checks use `err == goredis.Nil` instead of `errors.Is(err, goredis.Nil)`. While this works with the current go-redis v9 implementation (where `redis.Nil` is a simple sentinel), it is fragile: if any middleware, wrapper, or future go-redis version wraps errors, these comparisons will silently break, causing "not found" to be treated as real errors.

The Badger store correctly uses `errors.Is(err, badgerdb.ErrKeyNotFound)` everywhere.

**Fix:** Replace all five instances with:
```go
if errors.Is(err, goredis.Nil) {
```

---

### H4. [HIGH] NATS listener silently swallows decode errors

**File:** `/Users/nobelk/sources/reverb/pkg/cdc/nats/nats.go:63-68`

When `decodeMessage` fails (line 65), the error is silently discarded with a bare `return`. Malformed messages will be invisible to operators. In a production system, this makes debugging CDC integration failures extremely difficult.

**Fix:** Add logging or an error callback:
```go
sub, err := nc.Subscribe(l.subject, func(msg *natsclient.Msg) {
    event, err := decodeMessage(msg.Data)
    if err != nil {
        // At minimum, log it. Ideally accept a logger in the Listener struct.
        slog.Warn("nats: failed to decode message", "error", err, "subject", l.subject)
        return
    }
    ...
})
```

---

### H5. [HIGH] `Shutdown` in main.go uses `context.Background()` with no timeout

**File:** `/Users/nobelk/sources/reverb/cmd/reverb/main.go:154`

```go
if err := srv.Shutdown(context.Background()); err != nil {
```

This passes `context.Background()` (no deadline) to `Shutdown`. If any in-flight HTTP request hangs, the shutdown blocks forever and the process never exits. Note that `HTTPServer.Start()` (http.go:86-87) correctly uses a 10-second timeout for shutdown, but `main.go` bypasses `Start()` and calls `ListenAndServe()` + `Shutdown()` separately.

**Fix:**
```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
defer cancel()
if err := srv.Shutdown(shutdownCtx); err != nil {
```

---

### H6. [HIGH] `main.go` CDC consumer goroutine leaks on channel close

**File:** `/Users/nobelk/sources/reverb/cmd/reverb/main.go:111-122`

The goroutine that reads from `events` channel uses:
```go
case ev := <-events:
```
without checking the `ok` return value. When the NATS listener exits and the channel is garbage collected (it is never explicitly closed), this goroutine hangs forever, leaking. If the channel were closed, the goroutine would busy-loop processing zero-value `ChangeEvent`s, causing spurious invalidations.

**Fix:** Check the channel status:
```go
case ev, ok := <-events:
    if !ok {
        return
    }
```

---

## MEDIUM Issues

### M1. [MEDIUM] Both Badger and Redis `Stats()` never populate `TotalSizeBytes`

**File:** `/Users/nobelk/sources/reverb/pkg/store/badger/badger.go:316-351`
**File:** `/Users/nobelk/sources/reverb/pkg/store/redis/redis.go:235-276`

The `StoreStats` struct defines `TotalSizeBytes int64` (store.go:82), but neither implementation populates it. It is always returned as 0. Consumers relying on this field will get incorrect data.

**Fix:** For Badger, use `db.EstimateSize()` or accumulate value sizes during the scan. For Redis, use `MEMORY USAGE` per key or `DEBUG OBJECT`. At minimum, document that this field is not yet implemented.

---

### M2. [MEDIUM] `WithClock` option is applied too late -- `exactCache` and `semanticCache` already constructed with old clock

**File:** `/Users/nobelk/sources/reverb/pkg/reverb/client.go:90-130`

In `New()`, functional options are applied at line 127-129, **after** `exactCache` and `semanticCache` are constructed at lines 101-106 using `cfg.Clock`. If a caller passes `WithClock(mockClock)`, the client's `c.clock` field is updated, but the exact and semantic caches still use the original `cfg.Clock` (which defaults to `realClock{}`). This makes `WithClock` partially broken for testing.

**Fix:** Apply functional options before constructing the sub-caches, or restructure so sub-caches reference `c.clock` indirectly:
```go
// Apply options first to allow clock override
for _, opt := range opts {
    opt(c)
}
// Then construct sub-caches using the (potentially overridden) clock
exactCache := exact.New(s, c.clock)
semanticCache := semantic.New(embedder, vi, s, semanticCfg, c.clock)
```

---

### M3. [MEDIUM] `New()` always starts 3 background goroutines even when not needed

**File:** `/Users/nobelk/sources/reverb/pkg/reverb/client.go:132-153`

Three goroutines are always started:
- `invalidationLoop` (line 135) -- useless without a CDC listener, and its input channel is never written to unless WithCDCListener is provided
- `expiryReaper` (line 138) -- always useful
- `metricsUpdater` (line 141) -- only logs at Debug level; arguably unnecessary overhead

The `invalidationLoop` in particular holds open a goroutine + ticker indefinitely for no benefit when there is no CDC listener.

**Fix:** Only start `invalidationLoop` when a CDC listener is configured. Consider making `metricsUpdater` opt-in via an option.

---

### M4. [MEDIUM] `DeleteBatch` in both stores is not transactional

**File:** `/Users/nobelk/sources/reverb/pkg/store/badger/badger.go:218-228`
**File:** `/Users/nobelk/sources/reverb/pkg/store/redis/redis.go:145-155`

Both implementations loop and call `Delete` individually. If the 3rd deletion fails, the first 2 are already committed. This partial-deletion behavior may leave the system in an inconsistent state.

For Badger, this is easily fixable since BadgerDB supports multi-key transactions. For Redis, a pipeline or Lua script could be used.

**Fix:** For Badger, perform all deletes in a single `db.Update` transaction. For Redis, use a pipeline or Lua script.

---

### M5. [MEDIUM] Ollama `Dimensions()` returns 0 -- violates implicit Provider contract

**File:** `/Users/nobelk/sources/reverb/pkg/embedding/ollama/ollama.go:49-51`

`Dimensions()` returns `0`, which will cause issues for any caller that uses dimension count to pre-allocate vectors or validate embedding sizes. While the comment says "callers should not rely on this," the interface contract does not distinguish between providers that can and cannot report dimensions.

**Fix:** Either:
1. Make the first call to `Embed` cache the actual dimension size and return it from `Dimensions()`, or
2. Accept a `dimensions` parameter in the constructor, or
3. Add documentation to the `Provider` interface that `Dimensions()` may return 0 for dynamic-dimension providers.

---

### M6. [MEDIUM] `EmbedBatch` in Ollama serializes requests -- no concurrency

**File:** `/Users/nobelk/sources/reverb/pkg/embedding/ollama/ollama.go:60-73`

`EmbedBatch` calls `doEmbed` sequentially in a loop. For N texts, this takes N round-trips. This could be significantly slower than expected by callers who expect batching.

**Fix:** Use `errgroup` to parallelize requests with a concurrency limiter:
```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(8) // bounded concurrency
for i, text := range texts {
    g.Go(func() error {
        vec, err := p.doEmbed(ctx, text)
        if err != nil { return err }
        results[i] = vec
        return nil
    })
}
```

---

### M7. [MEDIUM] Ollama HTTP client has no timeout

**File:** `/Users/nobelk/sources/reverb/pkg/embedding/ollama/ollama.go:43`

`&http.Client{}` is created with no timeout. If the Ollama server hangs, the embedding call will block indefinitely, which can cascade into blocking Store operations and eventually the entire request pipeline.

**Fix:**
```go
client: &http.Client{Timeout: 30 * time.Second},
```

---

### M8. [MEDIUM] `io.ReadAll` with no size limit on Ollama response body

**File:** `/Users/nobelk/sources/reverb/pkg/embedding/ollama/ollama.go:100`

`io.ReadAll(resp.Body)` reads the entire response into memory with no size limit. A misbehaving or compromised Ollama server could return a massive response and cause OOM.

**Fix:** Use `io.LimitReader`:
```go
respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
```

---

## LOW Issues

### L1. [LOW] NATS listener has no reconnection or backoff logic

**File:** `/Users/nobelk/sources/reverb/pkg/cdc/nats/nats.go:57`

`natsclient.Connect(l.url)` uses default options, which include some built-in reconnection. However, there is no configuration for max reconnect attempts, reconnect wait, or custom error handlers. If the NATS server is temporarily unavailable at startup, `Start` fails immediately with no retry.

**Fix:** Configure NATS connection options:
```go
nc, err := natsclient.Connect(l.url,
    natsclient.MaxReconnects(-1),
    natsclient.ReconnectWait(2*time.Second),
    natsclient.ErrorHandler(func(_ *natsclient.Conn, _ *natsclient.Subscription, err error) {
        slog.Error("nats connection error", "error", err)
    }),
)
```

---

### L2. [LOW] `Scan` in Badger deserializes every entry even for non-matching namespaces

**File:** `/Users/nobelk/sources/reverb/pkg/store/badger/badger.go:284-314`

The scan iterates all `entry:*` keys and JSON-decodes every value, then filters by namespace. For a large store with many namespaces, this is O(total_entries) instead of O(entries_in_namespace).

**Fix:** Consider adding a namespace prefix index (e.g., `ns:{namespace}:{entryID}`) to enable efficient namespace-scoped iteration, or encode the namespace in the entry key.

---

### L3. [LOW] `Stats` in Badger also deserializes every entry unnecessarily

**File:** `/Users/nobelk/sources/reverb/pkg/store/badger/badger.go:316-351`

Same issue as L2: Stats iterates and deserializes all entries to count them and collect namespaces. Could use key-only iteration with a namespace extracted from a secondary index.

**Fix:** Same as L2 -- maintain a secondary namespace index.

---

### L4. [LOW] Missing `Content-Type` validation on Ollama HTTP responses

**File:** `/Users/nobelk/sources/reverb/pkg/embedding/ollama/ollama.go:109`

The response body is parsed as JSON without checking the `Content-Type` header. If Ollama returns an HTML error page, the JSON unmarshal error will be confusing.

**Fix:** Check `resp.Header.Get("Content-Type")` contains `application/json` before parsing.

---

### L5. [LOW] `writeJSON` in HTTP server ignores encode error

**File:** `/Users/nobelk/sources/reverb/pkg/server/http.go:328`

```go
_ = json.NewEncoder(w).Encode(v)
```

The encode error is silently discarded. While JSON encoding of known types rarely fails, if it does, the client receives a partial/malformed response.

**Fix:** Log the error:
```go
if err := json.NewEncoder(w).Encode(v); err != nil {
    s.logger.Error("failed to encode JSON response", "error", err)
}
```
(Note: `writeJSON` is a package-level function without access to the logger, so either make it a method on HTTPServer or accept a logger parameter.)

---

### L6. [LOW] Tracing spans are defined but never wired into the Client

**File:** `/Users/nobelk/sources/reverb/pkg/metrics/tracing.go` (entire file)

The `Tracer` struct and all span helpers (`StartLookupSpan`, `StartStoreSpan`, etc.) are implemented, but `client.go` never creates or uses a `Tracer`. The tracing code is dead code until integration is added.

**Fix:** Add a `WithTracer` option and instrument the `Lookup` and `Store` methods in client.go. This is not blocking but worth tracking.

---

## Positive Observations

1. **Excellent interface design.** The `Store`, `Provider`, and `Listener` interfaces are clean, minimal, and well-documented with precise nil/error contracts (e.g., "Returns nil, nil if not found").

2. **Compile-time interface checks.** Both `ollama.go` and `nats.go` use `var _ Interface = (*Impl)(nil)` for compile-time verification. Good practice.

3. **Conformance test suite.** The `store/conformance` package provides a shared test suite that all Store implementations run against. This is a mature testing pattern that prevents implementation drift.

4. **Consistent context cancellation checks.** Every Store method checks `ctx.Err()` at entry, preventing unnecessary work when the caller has already cancelled.

5. **Atomic metrics counters.** The `Collector` uses `sync/atomic.Int64` correctly throughout, with no data races for the core metrics path.

6. **Clean Prometheus integration.** The `PrometheusCollector` properly registers all metrics in a single call and returns errors on registration failure. The label structure is appropriate.

7. **Well-structured gRPC server.** The hand-rolled gRPC service descriptor avoids protoc dependency while maintaining a clean service interface. Input validation is thorough.

8. **HTTP server security.** `MaxBytesReader` is applied to all request bodies, `ReadHeaderTimeout` is set, and error messages do not leak internal details.

9. **Badger transactional consistency.** The Badger `Put` and `Delete` operations correctly handle index cleanup within a single transaction, maintaining referential integrity.

10. **Functional options pattern.** The `Option` pattern for `Client` configuration is idiomatic Go and allows clean extension without API breaks.

---

## Verdict: REQUEST CHANGES

Two CRITICAL race conditions in the Redis store (C1, C2) and six HIGH-severity issues must be addressed before this code is safe for production use. The most impactful fixes are:

1. Make Redis `IncrementHit` and `Put` atomic (C1, C2)
2. Fix the duplicate/leaked CDC goroutines in main.go (H1, H6)
3. Fix `context.Background()` in invalidation flush to prevent shutdown hangs (H2)
4. Use `errors.Is` for Redis nil checks (H3)
5. Add logging for NATS decode errors (H4)
6. Add shutdown timeout in main.go (H5)

The MEDIUM issues (M1-M8) should also be addressed but are not blocking for an initial merge.
