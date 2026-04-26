// Package limiter implements per-tenant token-bucket rate limiting and a
// bounded-concurrency semaphore. Both are designed for use in front of work
// that is expensive (embedding API calls, vector searches) so the service can
// shed load instead of building unbounded queues.
package limiter

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nobelk/reverb/internal/clock"
)

// ErrOverloaded is returned by ConcurrencyLimiter.Acquire when the in-flight
// cap is full and the queue is at capacity (or the caller waited longer than
// MaxWait without a slot opening up). Callers should translate this into an
// explicit overload signal to the client (HTTP 429 / 503, gRPC ResourceExhausted).
var ErrOverloaded = errors.New("limiter: overloaded")

// AnonymousTenant is the bucket key used when no authenticated tenant is
// available on the request (i.e. auth is disabled). Treating all unauthenticated
// callers as one tenant is intentional — operators can still cap a noisy
// single-tenant deployment without enabling full auth.
const AnonymousTenant = "_anonymous"

// TokenBucket is a single-tenant token-bucket rate limiter. Tokens accrue at
// `rate` per second up to `burst`; each Allow() call consumes one token. The
// bucket is safe for concurrent use.
type TokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	rate       float64 // tokens per second
	burst      float64 // bucket capacity
	clock      clock.Clock
}

// NewTokenBucket constructs a bucket starting full (i.e. `burst` tokens
// available). A non-positive rate disables the bucket entirely — every Allow()
// will return false. A non-positive burst disables it the same way.
func NewTokenBucket(rate float64, burst int, clk clock.Clock) *TokenBucket {
	if clk == nil {
		clk = clock.Real()
	}
	return &TokenBucket{
		tokens:     float64(burst),
		burst:      float64(burst),
		rate:       rate,
		lastRefill: clk.Now(),
		clock:      clk,
	}
}

// Allow consumes a token if one is available and reports whether the request
// is permitted.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refillLocked()
	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

// RetryAfter returns how long the caller should wait before the next request
// would be admitted. Returns 0 when a token is already available, and
// math.MaxInt64-scale durations are clamped at one minute so the value is
// always usable in a Retry-After header. If the bucket is permanently
// disabled (rate <= 0), returns 1 minute as a conservative fallback.
func (tb *TokenBucket) RetryAfter() time.Duration {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refillLocked()
	if tb.tokens >= 1 {
		return 0
	}
	if tb.rate <= 0 {
		return time.Minute
	}
	deficit := 1 - tb.tokens
	secs := deficit / tb.rate
	if secs > 60 || math.IsInf(secs, 0) || math.IsNaN(secs) {
		return time.Minute
	}
	return time.Duration(secs * float64(time.Second))
}

func (tb *TokenBucket) refillLocked() {
	now := tb.clock.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	if elapsed <= 0 {
		return
	}
	tb.tokens += elapsed * tb.rate
	if tb.tokens > tb.burst {
		tb.tokens = tb.burst
	}
	tb.lastRefill = now
}

// Registry holds one TokenBucket per tenant ID, lazily created on first use.
// All buckets share the same rate/burst configuration. The registry never
// evicts buckets — long-lived processes will accumulate one entry per
// tenant ID seen, which is bounded by the number of configured tenants.
type Registry struct {
	mu      sync.Mutex
	buckets map[string]*TokenBucket
	rate    float64
	burst   int
	clock   clock.Clock
}

// NewRegistry creates a registry that hands out per-tenant token buckets at
// the given rate and burst. Returns nil when rate <= 0 or burst <= 0; callers
// should treat a nil registry as "rate limiting disabled" and skip Allow().
func NewRegistry(rate float64, burst int, clk clock.Clock) *Registry {
	if rate <= 0 || burst <= 0 {
		return nil
	}
	if clk == nil {
		clk = clock.Real()
	}
	return &Registry{
		buckets: make(map[string]*TokenBucket),
		rate:    rate,
		burst:   burst,
		clock:   clk,
	}
}

// Allow checks whether the given tenant has tokens available. Returns true
// and 0 when permitted; false and a Retry-After duration when not.
func (r *Registry) Allow(tenantID string) (bool, time.Duration) {
	if tenantID == "" {
		tenantID = AnonymousTenant
	}
	r.mu.Lock()
	tb, ok := r.buckets[tenantID]
	if !ok {
		tb = NewTokenBucket(r.rate, r.burst, r.clock)
		r.buckets[tenantID] = tb
	}
	r.mu.Unlock()
	if tb.Allow() {
		return true, 0
	}
	return false, tb.RetryAfter()
}

// ConcurrencyLimiter enforces a hard cap on in-flight work and a bounded
// queue of waiters. When all slots are full and the queue is also full,
// Acquire returns ErrOverloaded immediately rather than blocking. This is the
// primitive used to protect the embedding pipeline from being overwhelmed.
type ConcurrencyLimiter struct {
	sem      chan struct{}
	waiters  atomic.Int64
	maxQueue int64
	maxWait  time.Duration
}

// NewConcurrencyLimiter constructs a limiter with the given in-flight cap,
// bounded-queue size, and per-caller max wait. Returns nil when maxInFlight
// <= 0; callers should treat a nil limiter as "concurrency cap disabled" and
// skip Acquire().
//
// maxQueue is the number of additional callers permitted to wait when all
// in-flight slots are full. maxWait is the longest a queued caller will block
// before giving up. A zero maxWait means non-blocking — overflow returns
// ErrOverloaded immediately.
func NewConcurrencyLimiter(maxInFlight, maxQueue int, maxWait time.Duration) *ConcurrencyLimiter {
	if maxInFlight <= 0 {
		return nil
	}
	if maxQueue < 0 {
		maxQueue = 0
	}
	return &ConcurrencyLimiter{
		sem:      make(chan struct{}, maxInFlight),
		maxQueue: int64(maxQueue),
		maxWait:  maxWait,
	}
}

// Acquire reserves a slot. On success, the caller must invoke Release exactly
// once. On ErrOverloaded or context cancellation, the caller must NOT call
// Release.
func (cl *ConcurrencyLimiter) Acquire(ctx context.Context) error {
	// Fast path: a slot is available right now, no queueing needed.
	select {
	case cl.sem <- struct{}{}:
		return nil
	default:
	}

	// All in-flight slots are full. Check the bounded queue.
	if cl.waiters.Add(1) > cl.maxQueue {
		cl.waiters.Add(-1)
		return ErrOverloaded
	}
	defer cl.waiters.Add(-1)

	if cl.maxWait <= 0 {
		// Non-blocking mode: try once more (a slot may have opened during the
		// queue check), then give up.
		select {
		case cl.sem <- struct{}{}:
			return nil
		default:
			return ErrOverloaded
		}
	}

	timer := time.NewTimer(cl.maxWait)
	defer timer.Stop()

	select {
	case cl.sem <- struct{}{}:
		return nil
	case <-timer.C:
		return ErrOverloaded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a previously acquired slot.
func (cl *ConcurrencyLimiter) Release() {
	<-cl.sem
}

// InFlight returns the current number of active slots. Intended for metrics.
func (cl *ConcurrencyLimiter) InFlight() int {
	return len(cl.sem)
}

// QueueDepth returns the current number of callers waiting for a slot.
// Intended for metrics.
func (cl *ConcurrencyLimiter) QueueDepth() int64 {
	return cl.waiters.Load()
}
