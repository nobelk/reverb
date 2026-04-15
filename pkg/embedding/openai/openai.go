package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/embedding"
)

const tracerName = "github.com/nobelk/reverb/pkg/embedding/openai"

// Compile-time check that Provider implements embedding.Provider.
var _ embedding.Provider = (*Provider)(nil)

// Config holds the configuration for the OpenAI embedding provider.
type Config struct {
	APIKey     string
	Model      string // e.g., "text-embedding-3-small"
	BaseURL    string // default "https://api.openai.com"
	Dimensions int    // e.g., 1536
}

// Provider implements embedding.Provider using the OpenAI embeddings API.
type Provider struct {
	cfg    Config
	client *http.Client
}

// embeddingRequest is the JSON body sent to the OpenAI embeddings endpoint.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingData represents a single embedding in the API response.
type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// embeddingResponse is the JSON response from the OpenAI embeddings endpoint.
type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Model string          `json:"model"`
	Usage json.RawMessage `json:"usage"`
}

// New creates a new OpenAI embedding provider with the given configuration.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.Model == "" {
		cfg.Model = "text-embedding-3-small"
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = 1536
	}
	return &Provider{
		cfg:    cfg,
		client: &http.Client{},
	}
}

// Dimensions returns the dimensionality of the embedding vectors.
func (p *Provider) Dimensions() int {
	return p.cfg.Dimensions
}

// Embed returns the embedding vector for a single text input.
func (p *Provider) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := p.doEmbed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 {
		return nil, fmt.Errorf("openai: empty response, no embeddings returned")
	}
	return vectors[0], nil
}

// EmbedBatch returns embedding vectors for multiple text inputs.
func (p *Provider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return p.doEmbed(ctx, texts)
}

// doEmbed performs the actual API call to the OpenAI embeddings endpoint.
func (p *Provider) doEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.embed.openai")
	defer span.End()
	span.SetAttributes(
		attribute.String("gen_ai.system", "reverb"),
		attribute.String("gen_ai.operation.name", "embed"),
		attribute.String("gen_ai.request.embedding.provider", "openai"),
		attribute.String("gen_ai.request.model", p.cfg.Model),
		attribute.Int("gen_ai.request.embedding.input_count", len(texts)),
	)

	reqBody := embeddingRequest{
		Model: p.cfg.Model,
		Input: texts,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: failed to marshal request: %w", err)
	}

	url := p.cfg.BaseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("openai: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB limit
	if err != nil {
		return nil, fmt.Errorf("openai: failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("openai: API returned status %d: %s", resp.StatusCode, string(respBody))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("openai: failed to decode response: %w", err)
	}

	if len(embResp.Data) == 0 {
		err := fmt.Errorf("openai: empty response, no embeddings returned")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	// Sort results by index to ensure correct ordering.
	vectors := make([][]float32, len(texts))
	for _, d := range embResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("openai: response index %d out of range [0, %d)", d.Index, len(texts))
		}
		vectors[d.Index] = d.Embedding
	}

	// Verify all slots were filled.
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("openai: missing embedding for input at index %d", i)
		}
	}

	return vectors, nil
}
