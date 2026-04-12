package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store"
)

// maxRequestBodySize limits the size of incoming request bodies to 1MB.
const maxRequestBodySize = 1 << 20

// HTTPServer exposes the Reverb cache over a JSON/REST API.
type HTTPServer struct {
	client *reverb.Client
	mux    *http.ServeMux
	server *http.Server
	logger *slog.Logger
}

// NewHTTPServer creates a new HTTPServer wired to the given Reverb client.
func NewHTTPServer(client *reverb.Client, addr string) *HTTPServer {
	logger := slog.Default()
	mux := http.NewServeMux()

	s := &HTTPServer{
		client: client,
		mux:    mux,
		logger: logger,
		server: &http.Server{
			Addr:              addr,
			Handler:           otelhttp.NewHandler(mux, "reverb-http"),
			ReadHeaderTimeout: 10 * time.Second,
		},
	}

	mux.HandleFunc("POST /v1/lookup", s.handleLookup)
	mux.HandleFunc("POST /v1/store", s.handleStore)
	mux.HandleFunc("POST /v1/invalidate", s.handleInvalidate)
	mux.HandleFunc("DELETE /v1/entries/{id}", s.handleDeleteEntry)
	mux.HandleFunc("GET /v1/stats", s.handleStats)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	return s
}

// ServeHTTP implements http.Handler so the server can be used with httptest.
func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
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
	ResponseMeta map[string]string `json:"response_meta,omitempty"`
	Sources      []sourceRefJSON   `json:"sources,omitempty"`
	TTLSeconds   int               `json:"ttl_seconds,omitempty"`
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
		Namespace: req.Namespace,
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
	if req.Response == "" {
		writeJSON(w, http.StatusBadRequest, errorResp{Error: "response is required"})
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
		Namespace:    req.Namespace,
		Prompt:       req.Prompt,
		ModelID:      req.ModelID,
		Response:     req.Response,
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

	writeJSON(w, http.StatusOK, statsResp{
		TotalEntries:       stats.TotalEntries,
		Namespaces:         stats.Namespaces,
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

	return &cacheEntryJSON{
		ID:           e.ID,
		CreatedAt:    e.CreatedAt,
		ExpiresAt:    e.ExpiresAt,
		Namespace:    e.Namespace,
		Prompt:       e.PromptText,
		ModelID:      e.ModelID,
		Response:     e.ResponseText,
		ResponseMeta: e.ResponseMeta,
		Sources:      sources,
		HitCount:     e.HitCount,
	}
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
