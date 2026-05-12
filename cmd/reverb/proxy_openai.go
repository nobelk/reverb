// proxy_openai.go: OpenAI-API-shaped reverse-proxy mode.
//
// When `cmd/reverb` is started with `--proxy openai --upstream <url>`, the
// binary serves OpenAI-shaped `/v1/chat/completions` (and, trivially,
// `/v1/embeddings`) instead of the native Reverb surface. Each request is
// hashed into a Reverb LookupRequest; on a hit the cached response is
// returned, on a miss the call is forwarded to <url>, and the response is
// stored before it is returned to the caller.
//
// The proxy honors RFC 9111 `Cache-Control: no-cache` (bypass the cache for
// reads) and `no-store` (skip the cache write after a forwarded miss). The
// caller's `Authorization` header is forwarded verbatim — the upstream key is
// the caller's key.
//
// Streaming is supported: when the request sets `"stream": true`, cached
// chunked responses replay as SSE; misses are streamed back as they arrive
// from the upstream and the deltas are accumulated into a chunked StoreRequest
// so the next caller hits the cache.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nobelk/reverb/pkg/reverb"
)

// openaiProxy is the OpenAI-shaped reverse-proxy http.Handler.
type openaiProxy struct {
	client      *reverb.Client
	upstream    *url.URL
	httpClient  *http.Client
	logger      *slog.Logger
	defaultNS   string
}

func newOpenAIProxy(client *reverb.Client, upstream string, logger *slog.Logger, defaultNS string) (*openaiProxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("upstream must include scheme and host (got %q)", upstream)
	}
	// Trim trailing slash so path joins are predictable.
	u.Path = strings.TrimRight(u.Path, "/")
	return &openaiProxy{
		client:     client,
		upstream:   u,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		logger:     logger,
		defaultNS:  defaultNS,
	}, nil
}

func (p *openaiProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", p.handleChatCompletions)
	mux.HandleFunc("POST /v1/embeddings", p.passthrough) // not cached — out of scope
	mux.HandleFunc("GET /healthz", p.handleHealthz)
	return mux
}

func (p *openaiProxy) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleChatCompletions implements the cache-aside flow for
// POST /v1/chat/completions.
func (p *openaiProxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "could not read request body")
		return
	}

	var req chatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	bypass, skipStore := parseCacheControl(r.Header.Get("Cache-Control"))
	prompt := canonicalize(body)

	if !bypass {
		hit, err := p.client.Lookup(r.Context(), reverb.LookupRequest{
			Namespace: p.defaultNS, Prompt: prompt, ModelID: req.Model,
		})
		if err == nil && hit.Hit {
			p.logger.Info("openai-proxy cache hit",
				"model", req.Model,
				"tier", hit.Tier,
				"similarity", hit.Similarity)
			w.Header().Set("X-Reverb-Cache", "HIT")
			w.Header().Set("X-Reverb-Tier", hit.Tier)
			if req.Stream {
				p.replayStream(w, hit.Entry)
			} else {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(hit.Entry.ResponseText))
			}
			return
		}
		if err != nil {
			p.logger.Error("openai-proxy lookup failed", "error", err)
		}
	}

	// Miss (or bypass): forward upstream.
	w.Header().Set("X-Reverb-Cache", "MISS")
	p.forwardAndStore(w, r, body, req, prompt, skipStore)
}

// forwardAndStore sends the request to the upstream and either streams the
// response back to the caller (and accumulates chunks for a cached store) or
// buffers the response and stores it as a single text body.
func (p *openaiProxy) forwardAndStore(
	w http.ResponseWriter, r *http.Request,
	body []byte, req chatCompletionRequest,
	prompt string, skipStore bool,
) {
	upstreamURL := *p.upstream
	upstreamURL.Path = strings.TrimRight(upstreamURL.Path, "/") + "/v1/chat/completions"

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "build upstream request: "+err.Error())
		return
	}
	for k, vs := range r.Header {
		switch strings.ToLower(k) {
		case "host", "content-length", "cache-control", "x-reverb-cache":
			continue
		}
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}
	upReq.Header.Set("Content-Type", "application/json")

	upResp, err := p.httpClient.Do(upReq)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "upstream unreachable: "+err.Error())
		return
	}
	defer upResp.Body.Close()

	for k, vs := range upResp.Header {
		// Strip hop-by-hop headers; net/http will set Content-Length itself.
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	if upResp.StatusCode >= 400 {
		_, _ = io.Copy(w, upResp.Body)
		return
	}

	if !req.Stream {
		buf, err := io.ReadAll(upResp.Body)
		if err != nil {
			p.logger.Error("openai-proxy: read upstream body failed", "error", err)
			return
		}
		_, _ = w.Write(buf)
		if !skipStore {
			if _, err := p.client.Store(context.WithoutCancel(r.Context()), reverb.StoreRequest{
				Namespace: p.defaultNS, Prompt: prompt, ModelID: req.Model,
				Response: string(buf),
			}); err != nil {
				p.logger.Error("openai-proxy: cache store failed", "error", err)
			}
		}
		return
	}

	// Streaming miss: tee the SSE stream to the client and accumulate chunks
	// for the cache write.
	flusher, _ := w.(http.Flusher)
	scanner := bufio.NewScanner(upResp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	var captured []reverb.ResponseChunk
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := fmt.Fprintln(w, line); err != nil {
			return
		}
		if line == "" {
			if flusher != nil {
				flusher.Flush()
			}
			continue
		}
		if delta, finish, ok := parseSSEDelta(line); ok {
			captured = append(captured, reverb.ResponseChunk{Delta: delta, FinishReason: finish})
		}
	}
	if flusher != nil {
		flusher.Flush()
	}
	if err := scanner.Err(); err != nil {
		p.logger.Error("openai-proxy: stream scan failed", "error", err)
		return
	}

	if !skipStore && len(captured) > 0 {
		if _, err := p.client.Store(context.WithoutCancel(r.Context()), reverb.StoreRequest{
			Namespace: p.defaultNS, Prompt: prompt, ModelID: req.Model,
			Chunks: captured,
		}); err != nil {
			p.logger.Error("openai-proxy: cache store failed", "error", err)
		}
	}
}

// passthrough forwards any non-cached endpoint (e.g. /v1/embeddings) to the
// upstream verbatim.
func (p *openaiProxy) passthrough(w http.ResponseWriter, r *http.Request) {
	upstreamURL := *p.upstream
	upstreamURL.Path = strings.TrimRight(upstreamURL.Path, "/") + r.URL.Path

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "build upstream request: "+err.Error())
		return
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}
	upResp, err := p.httpClient.Do(upReq)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "upstream unreachable: "+err.Error())
		return
	}
	defer upResp.Body.Close()
	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)
	_, _ = io.Copy(w, upResp.Body)
}

// replayStream replays a cached entry as an SSE stream. Cached chunks are
// emitted verbatim wrapped in a minimal OpenAI chat-completion delta envelope;
// non-streamed entries are emitted as a single delta + [DONE].
func (p *openaiProxy) replayStream(w http.ResponseWriter, entry *reverb.CacheEntry) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	emit := func(delta, finish string) {
		envelope := map[string]any{
			"id":     entry.ID,
			"object": "chat.completion.chunk",
			"choices": []map[string]any{
				{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nilIfEmpty(finish)},
			},
			"model": entry.ModelID,
		}
		raw, _ := json.Marshal(envelope)
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}

	if len(entry.Chunks) == 0 {
		emit(entry.ResponseText, "stop")
	} else {
		for _, c := range entry.Chunks {
			emit(c.Delta, c.FinishReason)
		}
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

// --- request shape & helpers ----------------------------------------------

// chatCompletionRequest is a minimal subset of the OpenAI chat-completions
// request shape — only the fields the proxy needs to make caching decisions.
type chatCompletionRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []any  `json:"messages"`
	Tools    []any  `json:"tools"`
}

// canonicalize produces a stable string for hashing. We hash the messages +
// model + tools fields. The full request body is used for stability — small
// fields like temperature and top_p that influence sampling are part of the
// identity of the request, so two requests with different temperatures must
// not collide.
func canonicalize(body []byte) string {
	// Use sha256 of the body; the SHA-256 fingerprint becomes the prompt that
	// Reverb itself hashes again. This double-hash is intentional: it keeps
	// the prompt opaque (no leakage of message content into Reverb's
	// PromptText debug field) and bounds prompt size.
	sum := sha256.Sum256(body)
	return "openai:" + hex.EncodeToString(sum[:])
}

// parseCacheControl extracts (bypass, skipStore) from a Cache-Control header.
// `no-cache` is a *read* directive (RFC 9111 §5.2.1.4) — bypass the cache for
// the lookup but still allow storing the response. `no-store` is a *write*
// directive (§5.2.1.5) — never store the response.
func parseCacheControl(h string) (bypass bool, skipStore bool) {
	if h == "" {
		return false, false
	}
	for _, raw := range strings.Split(h, ",") {
		token := strings.TrimSpace(strings.ToLower(raw))
		switch token {
		case "no-cache":
			bypass = true
		case "no-store":
			skipStore = true
		}
	}
	return
}

// parseSSEDelta extracts the content delta (and finish_reason if any) from an
// OpenAI streaming chunk line. Returns ok=false for non-data lines, [DONE],
// and unparseable payloads.
func parseSSEDelta(line string) (string, string, bool) {
	if !strings.HasPrefix(line, "data: ") {
		return "", "", false
	}
	payload := strings.TrimPrefix(line, "data: ")
	if payload == "[DONE]" {
		return "", "", false
	}
	var env struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return "", "", false
	}
	if len(env.Choices) == 0 {
		return "", "", false
	}
	c := env.Choices[0]
	finish := ""
	if c.FinishReason != nil {
		finish = *c.FinishReason
	}
	return c.Delta.Content, finish, true
}

func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"message":` + jsonString(msg) + `,"type":"reverb_proxy_error"}}`))
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
