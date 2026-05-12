package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

// TestProxy_CacheHitOnSecondCall verifies the value-prop loop: first request
// goes upstream, second hits the cache.
func TestProxy_CacheHitOnSecondCall(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fake","choices":[{"message":{"role":"assistant","content":"Paris."}}]}`))
	}))
	defer upstream.Close()

	client := newProxyClient(t)
	defer client.Close()

	proxy, err := newOpenAIProxy(client, upstream.URL, slog.Default(), "openai-proxy")
	require.NoError(t, err)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	body := strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"Capital of France?"}]}`)
	r1, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", body)
	require.NoError(t, err)
	defer r1.Body.Close()
	require.Equal(t, http.StatusOK, r1.StatusCode)
	assert.Equal(t, "MISS", r1.Header.Get("X-Reverb-Cache"))
	first, _ := io.ReadAll(r1.Body)

	// Second call with the same body should hit.
	body2 := strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"Capital of France?"}]}`)
	r2, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", body2)
	require.NoError(t, err)
	defer r2.Body.Close()
	require.Equal(t, http.StatusOK, r2.StatusCode)
	assert.Equal(t, "HIT", r2.Header.Get("X-Reverb-Cache"))
	second, _ := io.ReadAll(r2.Body)

	assert.Equal(t, string(first), string(second), "cached body must match upstream body")
	assert.Equal(t, int32(1), upstreamCalls.Load(), "upstream must be called exactly once across two identical requests")
}

// TestProxy_NoCacheBypass verifies that Cache-Control: no-cache forces a miss
// even when an entry is already cached.
func TestProxy_NoCacheBypass(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	}))
	defer upstream.Close()

	client := newProxyClient(t)
	defer client.Close()

	proxy, err := newOpenAIProxy(client, upstream.URL, slog.Default(), "openai-proxy")
	require.NoError(t, err)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	mkReq := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	// Warm the cache.
	r1, err := http.DefaultClient.Do(mkReq())
	require.NoError(t, err)
	r1.Body.Close()

	// no-cache → bypass: upstream must be hit again even though a cached
	// entry exists.
	req := mkReq()
	req.Header.Set("Cache-Control", "no-cache")
	r2, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	r2.Body.Close()
	assert.Equal(t, "MISS", r2.Header.Get("X-Reverb-Cache"))
	assert.Equal(t, int32(2), upstreamCalls.Load())
}

// TestProxy_NoStoreSkipsWrite ensures Cache-Control: no-store forwards but
// does not populate the cache.
func TestProxy_NoStoreSkipsWrite(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"x"}}]}`))
	}))
	defer upstream.Close()

	client := newProxyClient(t)
	defer client.Close()

	proxy, err := newOpenAIProxy(client, upstream.URL, slog.Default(), "openai-proxy")
	require.NoError(t, err)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	mkReq := func(cc string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		if cc != "" {
			req.Header.Set("Cache-Control", cc)
		}
		return req
	}

	// First call with no-store: forwards but does not cache.
	r1, _ := http.DefaultClient.Do(mkReq("no-store"))
	r1.Body.Close()

	// Second call without Cache-Control: should still miss (cache empty).
	r2, _ := http.DefaultClient.Do(mkReq(""))
	r2.Body.Close()
	assert.Equal(t, "MISS", r2.Header.Get("X-Reverb-Cache"))
	assert.Equal(t, int32(2), upstreamCalls.Load())
}

// TestProxy_AuthPassThrough confirms the caller's Authorization header
// reaches the upstream verbatim.
func TestProxy_AuthPassThrough(t *testing.T) {
	var seenAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	client := newProxyClient(t)
	defer client.Close()

	proxy, err := newOpenAIProxy(client, upstream.URL, slog.Default(), "openai-proxy")
	require.NoError(t, err)
	srv := httptest.NewServer(proxy.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"gpt-4","messages":[]}`)))
	req.Header.Set("Authorization", "Bearer sk-test-1234")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, "Bearer sk-test-1234", seenAuth)
}

// TestProxy_ParseCacheControl unit-tests the directive parser.
func TestProxy_ParseCacheControl(t *testing.T) {
	cases := []struct {
		in           string
		bypass, skip bool
	}{
		{"", false, false},
		{"no-cache", true, false},
		{"no-store", false, true},
		{"no-cache, no-store", true, true},
		{"NO-CACHE", true, false},
		{"max-age=0", false, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			b, s := parseCacheControl(c.in)
			assert.Equal(t, c.bypass, b)
			assert.Equal(t, c.skip, s)
		})
	}
}

// TestProxy_ParseSSEDelta covers happy path and edge cases.
func TestProxy_ParseSSEDelta(t *testing.T) {
	delta, finish, ok := parseSSEDelta(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`)
	require.True(t, ok)
	assert.Equal(t, "hi", delta)
	assert.Equal(t, "", finish)

	delta, finish, ok = parseSSEDelta(`data: {"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`)
	require.True(t, ok)
	assert.Equal(t, "", delta)
	assert.Equal(t, "stop", finish)

	_, _, ok = parseSSEDelta(`data: [DONE]`)
	assert.False(t, ok)

	_, _, ok = parseSSEDelta(`event: ping`)
	assert.False(t, ok)
}

// helper -------------------------------------------------------------------

func newProxyClient(t *testing.T) *reverb.Client {
	t.Helper()
	cfg := reverb.Config{
		DefaultNamespace:    "openai-proxy",
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	c, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New(0))
	require.NoError(t, err)
	return c
}

// silence "imported but not used" warnings for json — keeps the import alive
// for the canonicalize test if added later.
var _ = json.Marshal
