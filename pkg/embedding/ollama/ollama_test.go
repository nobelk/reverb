package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmbed(t *testing.T) {
	want := []float32{0.1, 0.2, 0.3}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/embeddings", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)

		var req embeddingRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "nomic-embed-text", req.Model)
		assert.Equal(t, "hello world", req.Prompt)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(embeddingResponse{Embedding: want})
	}))
	defer srv.Close()

	p := New(srv.URL, "nomic-embed-text")
	got, err := p.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestEmbedBatch(t *testing.T) {
	texts := []string{"foo", "bar", "baz"}
	calls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req embeddingRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		vec := []float32{float32(calls) * 0.1}
		json.NewEncoder(w).Encode(embeddingResponse{Embedding: vec})
	}))
	defer srv.Close()

	p := New(srv.URL, "nomic-embed-text")
	results, err := p.EmbedBatch(context.Background(), texts)
	require.NoError(t, err)
	assert.Len(t, results, 3)
	assert.Equal(t, 3, calls, "should make one HTTP call per text")
	assert.Equal(t, []float32{0.1}, results[0])
	assert.Equal(t, []float32{0.2}, results[1])
	assert.Equal(t, []float32{0.3}, results[2])
}

func TestEmbedBatchEmpty(t *testing.T) {
	p := New("http://localhost:11434", "nomic-embed-text")
	results, err := p.EmbedBatch(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, results)
}

func TestEmbedAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	p := New(srv.URL, "bad-model")
	_, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestEmbedEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{Embedding: nil})
	}))
	defer srv.Close()

	p := New(srv.URL, "nomic-embed-text")
	_, err := p.Embed(context.Background(), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty response")
}

func TestDimensions(t *testing.T) {
	p := New("http://localhost:11434", "nomic-embed-text")
	assert.Equal(t, 0, p.Dimensions())
}

func TestDefaultBaseURL(t *testing.T) {
	p := New("", "llama2")
	assert.Equal(t, "http://localhost:11434", p.baseURL)
}
