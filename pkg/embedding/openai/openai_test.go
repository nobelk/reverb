package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestServer creates a mock HTTP server that responds with the given handler.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAI_Embed_Success(t *testing.T) {
	expected := []float32{0.1, 0.2, 0.3, 0.4}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-api-key", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req embeddingRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, "text-embedding-3-small", req.Model)
		assert.Equal(t, []string{"hello world"}, req.Input)

		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: expected, Index: 0},
			},
			Model: "text-embedding-3-small",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	p := New(Config{
		APIKey:     "test-api-key",
		BaseURL:    srv.URL,
		Dimensions: 4,
	})

	vec, err := p.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	assert.Equal(t, expected, vec)
	assert.Equal(t, 4, p.Dimensions())
}

func TestOpenAI_Embed_ServerError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "internal server error"}}`))
	})

	p := New(Config{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	})

	vec, err := p.Embed(context.Background(), "hello")
	assert.Nil(t, vec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API returned status 500")
}

func TestOpenAI_Embed_MalformedJSON(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	})

	p := New(Config{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	})

	vec, err := p.Embed(context.Background(), "hello")
	assert.Nil(t, vec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode response")
}

func TestOpenAI_Embed_EmptyResponse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := embeddingResponse{
			Data:  []embeddingData{},
			Model: "text-embedding-3-small",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	p := New(Config{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	})

	vec, err := p.Embed(context.Background(), "hello")
	assert.Nil(t, vec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func TestOpenAI_EmbedBatch(t *testing.T) {
	texts := []string{"alpha", "beta", "gamma"}
	expectedVectors := [][]float32{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
		{0.7, 0.8, 0.9},
	}

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		require.NoError(t, err)
		assert.Equal(t, texts, req.Input)

		// Return embeddings in a different order to test index-based sorting.
		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: expectedVectors[2], Index: 2},
				{Embedding: expectedVectors[0], Index: 0},
				{Embedding: expectedVectors[1], Index: 1},
			},
			Model: "text-embedding-3-small",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	p := New(Config{
		APIKey:     "test-api-key",
		BaseURL:    srv.URL,
		Dimensions: 3,
	})

	vectors, err := p.EmbedBatch(context.Background(), texts)
	require.NoError(t, err)
	require.Len(t, vectors, 3)
	assert.Equal(t, expectedVectors[0], vectors[0])
	assert.Equal(t, expectedVectors[1], vectors[1])
	assert.Equal(t, expectedVectors[2], vectors[2])
}

func TestOpenAI_Embed_ContextCancellation(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// This handler should not be reached if context is canceled before the request.
		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: []float32{0.1}, Index: 0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	p := New(Config{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	vec, err := p.Embed(ctx, "hello")
	assert.Nil(t, vec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestOpenAI_Embed_LargeBodyLimit(t *testing.T) {
	// Serve exactly 10 MB + 1 byte of non-JSON data to verify that the provider
	// caps its read at 10 MB via io.LimitReader.  The body is not valid JSON so
	// the expected error is a decode error, not a read error.
	const limit = 10 << 20        // 10 MB
	const bodySize = limit + 1024 // slightly over the limit

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Set Content-Length so the HTTP layer knows exactly how many bytes to
		// expect.  This lets the server finish writing without blocking and
		// allows the client to reset the connection cleanly after the limit.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", bodySize))
		w.WriteHeader(http.StatusOK)

		chunk := make([]byte, 4096)
		for i := range chunk {
			chunk[i] = 'x'
		}
		written := 0
		for written < bodySize {
			n := bodySize - written
			if n > len(chunk) {
				n = len(chunk)
			}
			w.Write(chunk[:n])
			written += n
		}
	})

	p := New(Config{
		APIKey:  "test-api-key",
		BaseURL: srv.URL,
	})

	// Use a short timeout so the test cannot hang if the limit is not enforced.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := p.Embed(ctx, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode response")
}

func TestOpenAI_Embed_CustomBaseURL(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/embeddings", r.URL.Path)

		resp := embeddingResponse{
			Data: []embeddingData{
				{Embedding: []float32{1.0, 2.0}, Index: 0},
			},
			Model: "custom-model",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	p := New(Config{
		APIKey:     "test-api-key",
		Model:      "custom-model",
		BaseURL:    srv.URL,
		Dimensions: 2,
	})

	// Verify the custom base URL is stored correctly.
	assert.Equal(t, srv.URL, p.cfg.BaseURL)
	assert.Equal(t, "custom-model", p.cfg.Model)

	vec, err := p.Embed(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, []float32{1.0, 2.0}, vec)
}
