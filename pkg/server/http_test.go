package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func setupTestServer(t *testing.T) (*server.HTTPServer, *reverb.Client) {
	t.Helper()
	s := memory.New()
	vi := flat.New(0)
	embedder := fake.New(64)
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	client, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)
	srv := server.NewHTTPServer(client, ":0")
	return srv, client
}

// postJSON is a helper that issues a POST request with a JSON body.
func postJSON(t *testing.T, srv http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// storeEntry is a helper that stores an entry and returns the entry ID.
func storeEntry(t *testing.T, srv http.Handler) string {
	t.Helper()
	body := map[string]any{
		"namespace": "test-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
		"response":  "Go is a programming language.",
	}
	rec := postJSON(t, srv, "/v1/store", body)
	require.Equal(t, http.StatusCreated, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	id, ok := resp["id"].(string)
	require.True(t, ok, "expected id to be a string")
	require.NotEmpty(t, id)
	return id
}

func TestHTTP_Lookup_Hit(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Store an entry first.
	storeEntry(t, srv)

	// Lookup the same prompt.
	rec := postJSON(t, srv, "/v1/lookup", map[string]any{
		"namespace": "test-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
	})

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["hit"])
	assert.NotEmpty(t, resp["tier"])
	assert.NotNil(t, resp["entry"])
}

func TestHTTP_Lookup_Miss(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := postJSON(t, srv, "/v1/lookup", map[string]any{
		"namespace": "test-ns",
		"prompt":    "something never stored",
		"model_id":  "gpt-4",
	})

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["hit"])
}

func TestHTTP_Lookup_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/lookup", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "invalid JSON")
}

func TestHTTP_Lookup_MissingFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Missing namespace.
	rec := postJSON(t, srv, "/v1/lookup", map[string]any{
		"prompt":   "hello",
		"model_id": "gpt-4",
	})

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "namespace")
}

func TestHTTP_Store_Success(t *testing.T) {
	srv, _ := setupTestServer(t)

	rec := postJSON(t, srv, "/v1/store", map[string]any{
		"namespace": "test-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
		"response":  "Go is a programming language.",
	})

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp["id"])
	assert.NotEmpty(t, resp["created_at"])
}

func TestHTTP_Store_BadJSON(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/store", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHTTP_Invalidate_Success(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Store an entry with a source.
	body := map[string]any{
		"namespace": "test-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
		"response":  "Go is a programming language.",
		"sources": []map[string]string{
			{
				"source_id":    "doc-1",
				"content_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
	}
	rec := postJSON(t, srv, "/v1/store", body)
	require.Equal(t, http.StatusCreated, rec.Code)

	// Invalidate by source ID.
	rec = postJSON(t, srv, "/v1/invalidate", map[string]any{
		"source_id": "doc-1",
	})

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	count, ok := resp["invalidated_count"].(float64)
	require.True(t, ok)
	assert.GreaterOrEqual(t, int(count), 1)
}

func TestHTTP_DeleteEntry_Success(t *testing.T) {
	srv, _ := setupTestServer(t)

	entryID := storeEntry(t, srv)

	req := httptest.NewRequest(http.MethodDelete, "/v1/entries/"+entryID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestHTTP_Stats(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Store something so stats are non-trivial.
	storeEntry(t, srv)

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotNil(t, resp["total_entries"])
}

func TestHTTP_Healthz(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestHTTP_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHTTP_ContentType(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Test that JSON endpoints return application/json.
	endpoints := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/healthz", nil},
		{http.MethodGet, "/v1/stats", nil},
		{http.MethodPost, "/v1/lookup", map[string]any{
			"namespace": "ns",
			"prompt":    "hello",
			"model_id":  "m",
		}},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			var req *http.Request
			if ep.body != nil {
				b, err := json.Marshal(ep.body)
				require.NoError(t, err)
				req = httptest.NewRequest(ep.method, ep.path, bytes.NewReader(b))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(ep.method, ep.path, nil)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			ct := rec.Header().Get("Content-Type")
			assert.Contains(t, ct, "application/json", "expected application/json content type for %s %s", ep.method, ep.path)
		})
	}
}
