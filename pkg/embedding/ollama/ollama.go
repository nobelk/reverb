package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/org/reverb/pkg/embedding"
)

// Compile-time check that Provider implements embedding.Provider.
var _ embedding.Provider = (*Provider)(nil)

// Provider implements embedding.Provider using the local Ollama HTTP API.
type Provider struct {
	baseURL string
	model   string
	client  *http.Client
}

// embeddingRequest is the JSON body sent to the Ollama embeddings endpoint.
type embeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// embeddingResponse is the JSON response from the Ollama embeddings endpoint.
type embeddingResponse struct {
	Embedding []float32 `json:"embedding"`
}

// New creates a new Ollama embedding provider.
func New(baseURL, model string) *Provider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &Provider{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Dimensions returns 0 because Ollama dimension size depends on the model.
// Callers should not rely on this value for Ollama providers.
func (p *Provider) Dimensions() int {
	return 0
}

// Embed returns the embedding vector for a single text input.
func (p *Provider) Embed(ctx context.Context, text string) ([]float32, error) {
	return p.doEmbed(ctx, text)
}

// EmbedBatch returns embedding vectors for multiple text inputs.
// Ollama does not support native batching; calls are serialized.
func (p *Provider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	results := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := p.doEmbed(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("ollama: batch embed at index %d: %w", i, err)
		}
		results[i] = vec
	}
	return results, nil
}

// doEmbed performs the actual API call to the Ollama embeddings endpoint.
func (p *Provider) doEmbed(ctx context.Context, text string) ([]float32, error) {
	reqBody := embeddingRequest{
		Model:  p.model,
		Prompt: text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ollama: failed to marshal request: %w", err)
	}

	url := p.baseURL + "/api/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("ollama: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("ollama: failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		return nil, fmt.Errorf("ollama: failed to decode response: %w", err)
	}

	if len(embResp.Embedding) == 0 {
		return nil, fmt.Errorf("ollama: empty response, no embedding returned")
	}

	return embResp.Embedding, nil
}
