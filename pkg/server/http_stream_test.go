package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTP_LookupStream_Hit stores a chunked entry, replays it via
// /v1/lookup-stream, and verifies the SSE shape. It also verifies that
// /v1/lookup returns the legacy concatenated text for the same entry.
func TestHTTP_LookupStream_Hit(t *testing.T) {
	srv, _ := setupTestServer(t)

	// Store an entry with chunks. The handler should reconstruct `response`
	// from the chunk deltas, so legacy lookup callers see "Hello, world!".
	storeBody := map[string]any{
		"namespace": "test-ns",
		"prompt":    "greet me",
		"model_id":  "gpt-4",
		"chunks": []map[string]any{
			{"delta": "Hello"},
			{"delta": ", "},
			{"delta": "world!", "finish_reason": "stop"},
		},
	}
	rec := postJSON(t, srv, "/v1/store", storeBody)
	require.Equalf(t, http.StatusCreated, rec.Code, "store body=%s", rec.Body.String())

	// Stream-replay.
	streamReq := httptest.NewRequest(http.MethodPost, "/v1/lookup-stream",
		bytes.NewReader(mustJSON(t, map[string]any{
			"namespace": "test-ns",
			"prompt":    "greet me",
			"model_id":  "gpt-4",
		})))
	streamReq.Header.Set("Content-Type", "application/json")
	streamRec := httptest.NewRecorder()
	srv.ServeHTTP(streamRec, streamReq)

	require.Equal(t, http.StatusOK, streamRec.Code, "body=%s", streamRec.Body.String())
	assert.Equal(t, "text/event-stream", streamRec.Header().Get("Content-Type"))

	body := streamRec.Body.String()
	// Each delta should appear on its own data: line.
	assert.Contains(t, body, `data: {"delta":"Hello"`)
	assert.Contains(t, body, `data: {"delta":", "`)
	assert.Contains(t, body, `data: {"delta":"world!","finish_reason":"stop"}`)
	assert.True(t, strings.HasSuffix(strings.TrimRight(body, "\n"), "data: [DONE]"),
		"expected stream to terminate with [DONE], got tail=%q", lastN(body, 60))

	// And the legacy /v1/lookup response should carry the concatenated text.
	lookupRec := postJSON(t, srv, "/v1/lookup", map[string]any{
		"namespace": "test-ns",
		"prompt":    "greet me",
		"model_id":  "gpt-4",
	})
	require.Equal(t, http.StatusOK, lookupRec.Code)

	var lookup map[string]any
	require.NoError(t, json.Unmarshal(lookupRec.Body.Bytes(), &lookup))
	entry, ok := lookup["entry"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Hello, world!", entry["response"])
}

// TestHTTP_LookupStream_Miss returns 404 with the standard JSON error envelope.
func TestHTTP_LookupStream_Miss(t *testing.T) {
	srv, _ := setupTestServer(t)
	rec := postJSON(t, srv, "/v1/lookup-stream", map[string]any{
		"namespace": "test-ns",
		"prompt":    "never stored",
		"model_id":  "gpt-4",
	})
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}

// TestHTTP_LookupStream_LegacyEntry verifies that an entry stored without
// chunks still streams as a single chunk.
func TestHTTP_LookupStream_LegacyEntry(t *testing.T) {
	srv, _ := setupTestServer(t)
	storeEntry(t, srv) // namespace=test-ns prompt="What is Go?" response="Go is a programming language."

	streamReq := httptest.NewRequest(http.MethodPost, "/v1/lookup-stream",
		bytes.NewReader(mustJSON(t, map[string]any{
			"namespace": "test-ns",
			"prompt":    "What is Go?",
			"model_id":  "gpt-4",
		})))
	streamReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, streamReq)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data: {"delta":"Go is a programming language.","finish_reason":"stop"}`)
	assert.Contains(t, body, "data: [DONE]")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
