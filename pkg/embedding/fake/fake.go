package fake

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
)

// Provider is a deterministic embedding provider for tests.
// It generates embeddings by hashing the input text into a fixed-dimension
// vector. Semantically identical inputs always produce identical embeddings.
type Provider struct {
	dims int
}

func New(dims int) *Provider {
	return &Provider{dims: dims}
}

func (p *Provider) Embed(_ context.Context, text string) ([]float32, error) {
	return p.hashToVector(text), nil
}

func (p *Provider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, t := range texts {
		result[i] = p.hashToVector(t)
	}
	return result, nil
}

func (p *Provider) Dimensions() int { return p.dims }

// hashToVector produces a deterministic, L2-normalized vector from text.
func (p *Provider) hashToVector(text string) []float32 {
	vec := make([]float32, p.dims)
	for i := range vec {
		h := fnv.New64a()
		h.Write([]byte{byte(i), byte(i >> 8)})
		h.Write([]byte(text))
		bits := h.Sum64()
		vec[i] = float32(bits&0xFFFF) / 0xFFFF
	}
	// L2 normalize
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}

// FailingProvider always returns an error. Used to test graceful degradation.
type FailingProvider struct {
	Err  error
	dims int
}

func NewFailing(dims int, err error) *FailingProvider {
	if err == nil {
		err = ErrFakeEmbeddingFailure
	}
	return &FailingProvider{Err: err, dims: dims}
}

var ErrFakeEmbeddingFailure = errors.New("fake embedding failure")

func (p *FailingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, p.Err
}

func (p *FailingProvider) EmbedBatch(_ context.Context, _ []string) ([][]float32, error) {
	return nil, p.Err
}

func (p *FailingProvider) Dimensions() int { return p.dims }
