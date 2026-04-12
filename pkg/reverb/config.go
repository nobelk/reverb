package reverb

import (
	"errors"
	"time"
)

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Config holds Reverb configuration.
type Config struct {
	DefaultNamespace    string        `yaml:"default_namespace"`
	DefaultTTL          time.Duration `yaml:"default_ttl"`
	SimilarityThreshold float32       `yaml:"similarity_threshold"`
	SemanticTopK        int           `yaml:"semantic_top_k"`
	ScopeByModel        bool          `yaml:"scope_by_model"`

	Embedding EmbeddingConfig `yaml:"embedding"`
	Store     StoreConfig     `yaml:"store"`
	Vector    VectorConfig    `yaml:"vector"`
	CDC       CDCConfig       `yaml:"cdc"`
	Server    ServerConfig    `yaml:"server"`
	Metrics   MetricsConfig   `yaml:"metrics"`
	OTel      OTelConfig      `yaml:"otel"`

	// Clock — injectable for tests (defaults to real time)
	Clock Clock `yaml:"-"`
}

// EmbeddingConfig holds embedding provider configuration.
type EmbeddingConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`
	Dimensions int    `yaml:"dimensions"`
}

// StoreConfig holds store backend configuration.
type StoreConfig struct {
	Backend       string `yaml:"backend"`
	BadgerPath    string `yaml:"badger_path"`
	RedisAddr     string `yaml:"redis_addr"`
	RedisPassword string `yaml:"redis_password"`
	RedisDB       int    `yaml:"redis_db"`
	RedisPrefix   string `yaml:"redis_prefix"`
}

// VectorConfig holds vector index configuration.
type VectorConfig struct {
	Backend         string `yaml:"backend"`
	HNSWm           int    `yaml:"hnsw_m"`
	HNSWefConstruct int    `yaml:"hnsw_ef_construction"`
	HNSWefSearch    int    `yaml:"hnsw_ef_search"`
}

// CDCConfig holds CDC listener configuration.
type CDCConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Mode         string        `yaml:"mode"`
	WebhookAddr  string        `yaml:"webhook_addr"`
	WebhookPath  string        `yaml:"webhook_path"`
	PollInterval time.Duration `yaml:"poll_interval"`
	NatsURL      string        `yaml:"nats_url"`
	NatsSubject  string        `yaml:"nats_subject"`
}

// ServerConfig holds server configuration.
type ServerConfig struct {
	HTTPAddr string `yaml:"http_addr"`
	GRPCAddr string `yaml:"grpc_addr"`
}

// MetricsConfig holds observability configuration.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

// OTelConfig holds OpenTelemetry configuration.
type OTelConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Endpoint    string `yaml:"endpoint"`
	ServiceName string `yaml:"service_name"`
	Insecure    bool   `yaml:"insecure"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		DefaultNamespace:    "default",
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
		ScopeByModel:        true,
		Store: StoreConfig{
			Backend: "memory",
		},
		Vector: VectorConfig{
			Backend: "flat",
		},
	}
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.SimilarityThreshold < 0 || c.SimilarityThreshold > 1 {
		return errors.New("similarity_threshold must be between 0 and 1")
	}
	if c.SemanticTopK < 1 {
		return errors.New("semantic_top_k must be >= 1")
	}
	if c.DefaultTTL < 0 {
		return errors.New("default_ttl must be non-negative")
	}
	return nil
}

// ApplyDefaults fills in zero-value fields with defaults.
func (c *Config) ApplyDefaults() {
	if c.DefaultNamespace == "" {
		c.DefaultNamespace = "default"
	}
	if c.DefaultTTL == 0 {
		c.DefaultTTL = 24 * time.Hour
	}
	if c.SimilarityThreshold == 0 {
		c.SimilarityThreshold = 0.95
	}
	if c.SemanticTopK == 0 {
		c.SemanticTopK = 5
	}
	if c.Store.Backend == "" {
		c.Store.Backend = "memory"
	}
	if c.Vector.Backend == "" {
		c.Vector.Backend = "flat"
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
}
