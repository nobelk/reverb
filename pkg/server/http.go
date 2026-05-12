package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/nobelk/reverb/pkg/auth"
	"github.com/nobelk/reverb/pkg/limiter"
	"github.com/nobelk/reverb/pkg/metrics"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
)

// maxRequestBodySize limits the size of incoming request bodies to 1MB.
const maxRequestBodySize = 1 << 20

// ReadinessChecker reports whether the service is ready to serve traffic.
// A non-nil error indicates a dependency is unhealthy and the service should
// be removed from load-balancer rotation. The context is scoped to the probe
// and may carry a short deadline.
type ReadinessChecker func(ctx context.Context) error

// HTTPServer exposes the Reverb cache over a JSON/REST API.
type HTTPServer struct {
	client      *reverb.Client
	mux         *http.ServeMux
	handler     http.Handler // mux wrapped in rate limit + optional auth middleware
	server      *http.Server
	logger      *slog.Logger
	readyChecks []ReadinessChecker
	rateLimiter *limiter.Registry
	prom        *metrics.PrometheusCollector
}

// HTTPServerOption configures an HTTPServer at construction time.
type HTTPServerOption func(*HTTPServer)

// WithMetricsOnMux mounts promhttp on /metrics against the given gatherer.
// Pass the same registry used to register the PrometheusCollector. Under auth,
// /metrics is bypassed so Prometheus can scrape without a token — callers who
// want to gate /metrics should run a dedicated listener instead (see
// NewMetricsServer).
func WithMetricsOnMux(gatherer prometheus.Gatherer) HTTPServerOption {
	return func(s *HTTPServer) {
		s.mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	}
}

// WithReadinessCheck registers a probe called on GET /readyz. Multiple checks
// may be registered; readiness requires all to succeed. Keep checks cheap —
// the probe runs on every k8s tick.
func WithReadinessCheck(check ReadinessChecker) HTTPServerOption {
	return func(s *HTTPServer) {
		if check != nil {
			s.readyChecks = append(s.readyChecks, check)
		}
	}
}

// WithRateLimiter installs a per-tenant rate-limit middleware. Requests over
// the configured rate are rejected with 429 and a Retry-After header. The
// middleware skips /healthz, /readyz, and /metrics so probes and scrapes are
// never throttled. Pass nil to disable.
func WithRateLimiter(reg *limiter.Registry) HTTPServerOption {
	return func(s *HTTPServer) {
		s.rateLimiter = reg
	}
}

// WithMetricsCollector wires the Prometheus collector so the rate-limit
// middleware can record rejection counters.
func WithMetricsCollector(pc *metrics.PrometheusCollector) HTTPServerOption {
	return func(s *HTTPServer) {
		s.prom = pc
	}
}

// NewHTTPServer creates a new HTTPServer wired to the given Reverb client.
// When authn is non-nil, all endpoints except /healthz, /readyz, and /metrics
// require a valid Bearer token in the Authorization header.
func NewHTTPServer(client *reverb.Client, addr string, authn *auth.Authenticator, opts ...HTTPServerOption) *HTTPServer {
	logger := slog.Default()
	mux := http.NewServeMux()

	s := &HTTPServer{
		client: client,
		mux:    mux,
		logger: logger,
	}

	mux.HandleFunc("POST /v1/lookup", s.handleLookup)
	mux.HandleFunc("POST /v1/lookup-stream", s.handleLookupStream)
	mux.HandleFunc("POST /v1/store", s.handleStore)
	mux.HandleFunc("POST /v1/invalidate", s.handleInvalidate)
	mux.HandleFunc("DELETE /v1/entries/{id}", s.handleDeleteEntry)
	mux.HandleFunc("GET /v1/stats", s.handleStats)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	for _, opt := range opts {
		opt(s)
	}

	// Compose middleware chain after options so the rate limiter (and any
	// other configurable middleware) is fully populated. Order matters:
	// auth runs first so the rate limiter sees the tenant ID.
	var handler http.Handler = mux
	if s.rateLimiter != nil {
		handler = rateLimitMiddleware(s.rateLimiter, s.prom)(handler)
	}
	if authn != nil {
		handler = auth.HTTPMiddleware(authn)(handler)
	}
	s.handler = handler
	s.server = &http.Server{
		Addr:              addr,
		Handler:           otelhttp.NewHandler(handler, "reverb-http"),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s
}

// rateLimitMiddleware enforces a per-tenant token-bucket rate limit. It runs
// after the auth middleware so the tenant ID is already in the context. The
// special paths /healthz, /readyz, and /metrics are never throttled — they
// must remain answerable for probes and scrapers even when the service is
// otherwise overloaded.
func rateLimitMiddleware(reg *limiter.Registry, pc *metrics.PrometheusCollector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				next.ServeHTTP(w, r)
				return
			}

			tenantKey := limiter.AnonymousTenant
			if t, ok := auth.TenantFromContext(r.Context()); ok {
				tenantKey = t.ID
			}

			ok, retryAfter := reg.Allow(tenantKey)
			if !ok {
				if pc != nil {
					pc.RejectedRequestsTotal.WithLabelValues("http", "rate_limit").Inc()
				}
				// Round Retry-After up to whole seconds per RFC 7231 — clients
				// expect an integer count of seconds in the header. Always
				// advertise at least 1 to avoid a client retry loop.
				secs := max(int(retryAfter.Round(time.Second)/time.Second), 1)
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				writeJSON(w, http.StatusTooManyRequests, errorResp{Error: "rate limit exceeded"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ServeHTTP implements http.Handler so the server can be used with httptest.
// This routes through any configured middleware (e.g. auth).
func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// ListenAndServe starts the HTTP server. It blocks until the server is shut down.
func (s *HTTPServer) ListenAndServe() error {
	s.logger.Info("starting HTTP server", "addr", s.server.Addr)
	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// Start begins listening and blocks until the context is cancelled, then
// performs a graceful shutdown. This is the main entry point used by cmd/reverb.
func (s *HTTPServer) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	}
}

// --- request/response types ------------------------------------------------

type lookupReq struct {
	Namespace string `json:"namespace"`
	Prompt    string `json:"prompt"`
	ModelID   string `json:"model_id"`
}

type lookupResp struct {
	Hit        bool              `json:"hit"`
	Tier       string            `json:"tier,omitempty"`
	Similarity float32           `json:"similarity,omitempty"`
	Entry      *cacheEntryJSON   `json:"entry,omitempty"`
}

type cacheEntryJSON struct {
	ID           string            `json:"id"`
	CreatedAt    time.Time         `json:"created_at"`
	ExpiresAt    time.Time         `json:"expires_at,omitempty"`
	Namespace    string            `json:"namespace"`
	Prompt       string            `json:"prompt"`
	ModelID      string            `json:"model_id"`
	Response     string            `json:"response"`
	Chunks       []chunkJSON       `json:"chunks,omitempty"`
	ResponseMeta map[string]string `json:"response_meta,omitempty"`
	Sources      []sourceRefJSON   `json:"sources,omitempty"`
	HitCount     int64             `json:"hit_count"`
}

type sourceRefJSON struct {
	SourceID    string `json:"source_id"`
	ContentHash string `json:"content_hash"`
}

type storeReq struct {
	Namespace    string            `json:"namespace"`
	Prompt       string            `json:"prompt"`
	ModelID      string            `json:"model_id"`
	Response     string            `json:"response"`
	Chunks       []chunkJSON       `json:"chunks,omitempty"`
	ResponseMeta map[string]string `json:"response_meta,omitempty"`
	Sources      []sourceRefJSON   `json:"sources,omitempty"`
	TTLSeconds   int               `json:"ttl_seconds,omitempty"`
}

type chunkJSON struct {
	Delta        string `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

type storeResp struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type invalidateReq struct {
	SourceID string `json:"source_id"`
}

type invalidateResp struct {
	InvalidatedCount int `json:"invalidated_count"`
}

type statsResp struct {
	TotalEntries       int64    `json:"total_entries"`
	Namespaces         []string `json:"namespaces"`
	ExactHitsTotal     int64    `json:"exact_hits_total"`
	SemanticHitsTotal  int64    `json:"semantic_hits_total"`
	MissesTotal        int64    `json:"misses_total"`
	InvalidationsTotal int64    `json:"invalidations_total"`
	HitRate            float64  `json:"hit_rate"`
}

type healthResp struct {
	Status string `json:"status"`
}

type readyResp struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

type errorResp struct {
	Error string `json:"error"`
}

// --- handlers --------------------------------------------------------------

func (s *HTTPServer) handleLookup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req lookupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "invalid JSON: " + err.Error()})
		return
	}

	if req.Namespace == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "namespace is required"})
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "prompt is required"})
		return
	}

	result, err := s.client.Lookup(r.Context(), reverb.LookupRequest{
		Namespace: auth.ScopedNamespace(r.Context(), req.Namespace),
		Prompt:    req.Prompt,
		ModelID:   req.ModelID,
	})
	if err != nil {
		s.logger.Error("lookup failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}

	resp := lookupResp{
		Hit:        result.Hit,
		Tier:       result.Tier,
		Similarity: result.Similarity,
	}
	if result.Entry != nil {
		resp.Entry = toCacheEntryJSON(result.Entry)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleLookupStream replays a cached entry as Server-Sent Events. Each
// chunk in the entry's Chunks slice becomes a single `data:` line; entries
// stored without chunks are emitted as one chunk carrying the full response
// text. The stream terminates with `data: [DONE]` per the OpenAI SSE
// convention. Cache miss returns 404 with the standard error envelope. A
// 15-second comment-line keepalive runs concurrently with the chunk stream
// so intermediate proxies don't time out long replays.
func (s *HTTPServer) handleLookupStream(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req lookupReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "invalid JSON: " + err.Error()})
		return
	}
	if req.Namespace == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "namespace is required"})
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "prompt is required"})
		return
	}

	result, err := s.client.Lookup(r.Context(), reverb.LookupRequest{
		Namespace: auth.ScopedNamespace(r.Context(), req.Namespace),
		Prompt:    req.Prompt,
		ModelID:   req.ModelID,
	})
	if err != nil {
		s.logger.Error("lookup-stream lookup failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}
	if !result.Hit {
		writeJSON(w, http.StatusNotFound, errorResp{Error: "no cached response"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	chunks := result.Entry.Chunks
	if len(chunks) == 0 {
		chunks = []store.ResponseChunk{{Delta: result.Entry.ResponseText, FinishReason: "stop"}}
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	send := func(payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	for _, ch := range chunks {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		default:
		}
		if err := send(chunkJSON{Delta: ch.Delta, FinishReason: ch.FinishReason}); err != nil {
			s.logger.Error("lookup-stream send failed", "error", err)
			return
		}
	}

	// OpenAI SSE convention — terminator sentinel.
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		return
	}
	flusher.Flush()
}

func (s *HTTPServer) handleStore(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req storeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "invalid JSON: " + err.Error()})
		return
	}

	if req.Namespace == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "namespace is required"})
		return
	}
	if req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "prompt is required"})
		return
	}
	if req.Response == "" && len(req.Chunks) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "response or chunks is required"})
		return
	}

	sources, err := convertSources(req.Sources)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: err.Error()})
		return
	}

	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	entry, err := s.client.Store(r.Context(), reverb.StoreRequest{
		Namespace:    auth.ScopedNamespace(r.Context(), req.Namespace),
		Prompt:       req.Prompt,
		ModelID:      req.ModelID,
		Response:     req.Response,
		Chunks:       chunksFromJSON(req.Chunks),
		ResponseMeta: req.ResponseMeta,
		Sources:      sources,
		TTL:          ttl,
	})
	if err != nil {
		s.logger.Error("store failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}

	writeJSON(w, http.StatusCreated, storeResp{
		ID:        entry.ID,
		CreatedAt: entry.CreatedAt,
	})
}

func (s *HTTPServer) handleInvalidate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req invalidateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "invalid JSON: " + err.Error()})
		return
	}

	if req.SourceID == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "source_id is required"})
		return
	}

	count, err := s.client.Invalidate(r.Context(), req.SourceID)
	if err != nil {
		s.logger.Error("invalidate failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, invalidateResp{InvalidatedCount: count})
}

func (s *HTTPServer) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	entryID := r.PathValue("id")
	if entryID == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "entry id is required"})
		return
	}

	// Tenant ownership check: when auth is active, verify the entry's
	// namespace belongs to the requesting tenant before deleting.
	if tenant, ok := auth.TenantFromContext(r.Context()); ok {
		entry, err := s.client.GetEntry(r.Context(), entryID)
		if err != nil {
			s.logger.Error("delete entry: get failed", "entry_id", entryID, "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
			return
		}
		// If entry doesn't exist or belongs to another tenant, return 404
		// without revealing which case it is.
		if entry == nil || !auth.NamespaceBelongsToTenant(tenant.ID, entry.Namespace) {
			writeJSON(w, http.StatusNotFound, errorResp{Error: "entry not found"})
			return
		}
	}

	if err := s.client.InvalidateEntry(r.Context(), entryID); err != nil {
		s.logger.Error("delete entry failed", "entry_id", entryID, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.client.Stats(r.Context())
	if err != nil {
		s.logger.Error("stats failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
		return
	}

	namespaces := stats.Namespaces
	totalEntries := stats.TotalEntries
	if tenant, ok := auth.TenantFromContext(r.Context()); ok {
		var (
			filtered []string
			scoped   []string // pre-prefix versions to count against the store
		)
		for _, ns := range stats.Namespaces {
			if unscoped, match := auth.UnscopeNamespace(tenant.ID, ns); match {
				filtered = append(filtered, unscoped)
				scoped = append(scoped, ns)
			}
		}
		namespaces = filtered

		var tenantTotal int64
		for _, ns := range scoped {
			n, err := s.client.CountInNamespace(r.Context(), ns)
			if err != nil {
				s.logger.Error("stats: count failed", "namespace", ns, "error", err)
				writeJSON(w, http.StatusInternalServerError, errorResp{Error: "internal error"})
				return
			}
			tenantTotal += n
		}
		totalEntries = tenantTotal
	}

	writeJSON(w, http.StatusOK, statsResp{
		TotalEntries:       totalEntries,
		Namespaces:         namespaces,
		ExactHitsTotal:     stats.ExactHitsTotal,
		SemanticHitsTotal:  stats.SemanticHitsTotal,
		MissesTotal:        stats.MissesTotal,
		InvalidationsTotal: stats.InvalidationsTotal,
		HitRate:            stats.HitRate,
	})
}

func (s *HTTPServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResp{Status: "ok"})
}

// handleReadyz runs every registered ReadinessChecker with a 2s deadline and
// returns 200 only when all succeed. Failures are reported per-check in the
// body so operators can see which dependency is the cause.
func (s *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	failed := make(map[string]string)
	for i, check := range s.readyChecks {
		if err := check(ctx); err != nil {
			failed[fmt.Sprintf("check_%d", i)] = err.Error()
		}
	}

	if len(failed) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, readyResp{
			Status: "not_ready",
			Checks: failed,
		})
		return
	}
	writeJSON(w, http.StatusOK, readyResp{Status: "ready"})
}

// --- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func toCacheEntryJSON(e *store.CacheEntry) *cacheEntryJSON {
	sources := make([]sourceRefJSON, len(e.SourceHashes))
	for i, s := range e.SourceHashes {
		sources[i] = sourceRefJSON{
			SourceID:    s.SourceID,
			ContentHash: hex.EncodeToString(s.ContentHash[:]),
		}
	}

	var chunks []chunkJSON
	if len(e.Chunks) > 0 {
		chunks = make([]chunkJSON, len(e.Chunks))
		for i, ch := range e.Chunks {
			chunks[i] = chunkJSON{Delta: ch.Delta, FinishReason: ch.FinishReason}
		}
	}

	return &cacheEntryJSON{
		ID:           e.ID,
		CreatedAt:    e.CreatedAt,
		ExpiresAt:    e.ExpiresAt,
		Namespace:    e.Namespace,
		Prompt:       e.PromptText,
		ModelID:      e.ModelID,
		Response:     e.ResponseText,
		Chunks:       chunks,
		ResponseMeta: e.ResponseMeta,
		Sources:      sources,
		HitCount:     e.HitCount,
	}
}

func chunksFromJSON(in []chunkJSON) []store.ResponseChunk {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.ResponseChunk, len(in))
	for i, c := range in {
		out[i] = store.ResponseChunk{Delta: c.Delta, FinishReason: c.FinishReason}
	}
	return out
}

func convertSources(jsonSources []sourceRefJSON) ([]store.SourceRef, error) {
	if len(jsonSources) == 0 {
		return nil, nil
	}
	refs := make([]store.SourceRef, len(jsonSources))
	for i, js := range jsonSources {
		refs[i].SourceID = js.SourceID
		if js.ContentHash != "" {
			decoded, err := hex.DecodeString(strings.TrimPrefix(js.ContentHash, "0x"))
			if err != nil {
				return nil, fmt.Errorf("invalid content_hash for source %q: %w", js.SourceID, err)
			}
			if len(decoded) != sha256.Size {
				return nil, fmt.Errorf("content_hash for source %q must be %d bytes (got %d)", js.SourceID, sha256.Size, len(decoded))
			}
			copy(refs[i].ContentHash[:], decoded)
		}
	}
	return refs, nil
}
