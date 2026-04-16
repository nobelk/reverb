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

	"github.com/nobelk/reverb/pkg/auth"
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
	srv := server.NewHTTPServer(client, ":0", nil)
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

// --- auth tests --------------------------------------------------------------

func setupAuthServer(t *testing.T) (*server.HTTPServer, *auth.Authenticator) {
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

	authn, err := auth.NewAuthenticator(reverb.AuthConfig{
		Tenants: []reverb.Tenant{
			{ID: "tenant-a", APIKeys: []string{"key-a"}},
			{ID: "tenant-b", APIKeys: []string{"key-b"}},
		},
	})
	require.NoError(t, err)
	srv := server.NewHTTPServer(client, ":0", authn)
	return srv, authn
}

func authedPost(t *testing.T, srv http.Handler, path string, apiKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHTTP_Auth_Unauthorized(t *testing.T) {
	srv, _ := setupAuthServer(t)

	// No token → 401.
	rec := authedPost(t, srv, "/v1/lookup", "", map[string]any{
		"namespace": "ns", "prompt": "hello",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// Invalid token → 401.
	rec = authedPost(t, srv, "/v1/lookup", "bad-key", map[string]any{
		"namespace": "ns", "prompt": "hello",
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHTTP_Auth_HealthzBypassesAuth(t *testing.T) {
	srv, _ := setupAuthServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHTTP_Auth_TenantIsolation(t *testing.T) {
	srv, _ := setupAuthServer(t)

	// Tenant A stores an entry.
	rec := authedPost(t, srv, "/v1/store", "key-a", map[string]any{
		"namespace": "shared-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
		"response":  "A language by Google.",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	// Tenant A can look it up.
	rec = authedPost(t, srv, "/v1/lookup", "key-a", map[string]any{
		"namespace": "shared-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["hit"])

	// Tenant B cannot see it — same namespace and prompt, but different tenant.
	rec = authedPost(t, srv, "/v1/lookup", "key-b", map[string]any{
		"namespace": "shared-ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["hit"])
}

func TestHTTP_Auth_DeleteEntry_TenantOwnership(t *testing.T) {
	srv, _ := setupAuthServer(t)

	// Tenant A stores an entry.
	rec := authedPost(t, srv, "/v1/store", "key-a", map[string]any{
		"namespace": "ns",
		"prompt":    "What is Go?",
		"model_id":  "gpt-4",
		"response":  "A language.",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	var storeResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &storeResp))
	entryID := storeResp["id"].(string)

	// Tenant B tries to delete it → 404 (without revealing existence).
	req := httptest.NewRequest(http.MethodDelete, "/v1/entries/"+entryID, nil)
	req.Header.Set("Authorization", "Bearer key-b")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Entry should still exist for tenant A.
	rec = authedPost(t, srv, "/v1/lookup", "key-a", map[string]any{
		"namespace": "ns", "prompt": "What is Go?", "model_id": "gpt-4",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var lookupResp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &lookupResp))
	assert.Equal(t, true, lookupResp["hit"])

	// Tenant A can delete it.
	req = httptest.NewRequest(http.MethodDelete, "/v1/entries/"+entryID, nil)
	req.Header.Set("Authorization", "Bearer key-a")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestHTTP_Auth_DeleteEntry_NotFound(t *testing.T) {
	srv, _ := setupAuthServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/v1/entries/nonexistent-id", nil)
	req.Header.Set("Authorization", "Bearer key-a")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHTTP_Auth_Stats_TotalEntriesScoped(t *testing.T) {
	srv, _ := setupAuthServer(t)

	// Tenant A stores 2 entries in different namespaces.
	for i, p := range []string{"prompt-1", "prompt-2"} {
		rec := authedPost(t, srv, "/v1/store", "key-a", map[string]any{
			"namespace": "ns-a",
			"prompt":    p,
			"model_id":  "gpt-4",
			"response":  "resp",
		})
		require.Equalf(t, http.StatusCreated, rec.Code, "entry %d", i)
	}
	// Tenant B stores 1 entry.
	rec := authedPost(t, srv, "/v1/store", "key-b", map[string]any{
		"namespace": "ns-b",
		"prompt":    "different",
		"model_id":  "gpt-4",
		"response":  "resp",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	// Tenant A's stats should show 2 entries.
	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer key-a")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var statsA map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &statsA))
	assert.EqualValues(t, 2, statsA["total_entries"])
	assert.ElementsMatch(t, []any{"ns-a"}, statsA["namespaces"])

	// Tenant B's stats should show 1 entry.
	req = httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer key-b")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var statsB map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &statsB))
	assert.EqualValues(t, 1, statsB["total_entries"])
	assert.ElementsMatch(t, []any{"ns-b"}, statsB["namespaces"])
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
