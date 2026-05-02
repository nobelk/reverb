package reverb

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/nobelk/reverb/internal/clock"
)

// Clock abstracts time for testability. It is an alias of internal/clock.Clock
// so all backends share one interface; the public name is preserved for
// callers that already depend on reverb.Clock.
type Clock = clock.Clock

// Config holds Reverb configuration.
type Config struct {
	DefaultNamespace    string        `yaml:"default_namespace"`
	DefaultTTL          time.Duration `yaml:"default_ttl"`
	SimilarityThreshold float32       `yaml:"similarity_threshold"`
	SemanticTopK        int           `yaml:"semantic_top_k"`
	ScopeByModel        bool          `yaml:"scope_by_model"`

	Embedding   EmbeddingConfig   `yaml:"embedding"`
	Store       StoreConfig       `yaml:"store"`
	Vector      VectorConfig      `yaml:"vector"`
	CDC         CDCConfig         `yaml:"cdc"`
	Server      ServerConfig      `yaml:"server"`
	Auth        AuthConfig        `yaml:"auth"`
	Metrics     MetricsConfig     `yaml:"metrics"`
	OTel        OTelConfig        `yaml:"otel"`
	RateLimit   RateLimitConfig   `yaml:"rate_limit"`
	Concurrency ConcurrencyConfig `yaml:"concurrency"`

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

	// RebuildVectorIndexOnStartup, when true, scans the store at boot and
	// re-adds every non-expired entry's embedding to the vector index.
	// See reverb.WithRebuildVectorIndex for trade-offs.
	RebuildVectorIndexOnStartup bool `yaml:"rebuild_vector_index_on_startup"`
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

// AuthConfig holds API key authentication configuration.
type AuthConfig struct {
	Enabled bool     `yaml:"enabled"`
	Tenants []Tenant `yaml:"tenants"`
}

// Tenant maps one or more API keys to a tenant identity.
type Tenant struct {
	ID      string   `yaml:"id"`
	Name    string   `yaml:"name"`
	APIKeys []string `yaml:"api_keys"`
}

// RateLimitConfig holds per-tenant request rate-limit configuration.
//
// When Enabled, each tenant gets an independent token bucket of size Burst
// that refills at RequestsPerSecond tokens/sec. Requests beyond the bucket
// are rejected with HTTP 429 / gRPC ResourceExhausted and a Retry-After hint.
// When auth is disabled, all unauthenticated callers share a single
// "_anonymous" bucket — set conservatively for that case.
type RateLimitConfig struct {
	Enabled           bool    `yaml:"enabled"`
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

// ConcurrencyConfig holds the embedding-pipeline concurrency cap.
//
// MaxInFlight bounds the number of concurrent embedding-provider calls.
// MaxQueued is the depth of the bounded waiter queue once in-flight is
// saturated; callers beyond that are rejected immediately. MaxQueueWait is
// the longest a queued caller will wait before giving up (translated into
// HTTP 503 / gRPC Unavailable upstream). When MaxInFlight is 0 the cap is
// disabled and the embedder is used directly.
type ConcurrencyConfig struct {
	MaxInFlight  int           `yaml:"max_in_flight"`
	MaxQueued    int           `yaml:"max_queued"`
	MaxQueueWait time.Duration `yaml:"max_queue_wait"`
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
	if c.RateLimit.Enabled {
		if c.RateLimit.RequestsPerSecond <= 0 {
			return errors.New("rate_limit.requests_per_second must be > 0 when rate_limit is enabled")
		}
		if c.RateLimit.Burst <= 0 {
			return errors.New("rate_limit.burst must be > 0 when rate_limit is enabled")
		}
	}
	if c.Concurrency.MaxInFlight < 0 {
		return errors.New("concurrency.max_in_flight must be >= 0")
	}
	if c.Concurrency.MaxQueued < 0 {
		return errors.New("concurrency.max_queued must be >= 0")
	}
	if c.Concurrency.MaxQueueWait < 0 {
		return errors.New("concurrency.max_queue_wait must be non-negative")
	}
	if c.Auth.Enabled {
		if len(c.Auth.Tenants) == 0 {
			return errors.New("auth.tenants must contain at least one tenant when auth is enabled")
		}
		seen := make(map[string]bool)
		ids := make(map[string]bool)
		for _, t := range c.Auth.Tenants {
			if t.ID == "" {
				return errors.New("auth.tenants: tenant id must not be empty")
			}
			if ids[t.ID] {
				return fmt.Errorf("auth.tenants: duplicate tenant id %q", t.ID)
			}
			ids[t.ID] = true
			if len(t.APIKeys) == 0 {
				return fmt.Errorf("auth.tenants: tenant %q must have at least one api_key", t.ID)
			}
			for _, k := range t.APIKeys {
				if seen[k] {
					return fmt.Errorf("auth.tenants: duplicate api key in tenant %q", t.ID)
				}
				seen[k] = true
			}
		}
		if c.Server.HTTPAddr == "" && c.Server.GRPCAddr == "" {
			return errors.New("auth is enabled but neither server.http_addr nor server.grpc_addr is set — auth would protect nothing")
		}
	}

	return c.validateListenAddrs()
}

// validateListenAddrs parses each non-empty listen address and rejects
// configurations where two of them would bind the same socket. Two addresses
// conflict when their ports match and the hosts are equal (or both are
// wildcard — empty/"0.0.0.0"/"::"). Distinct bound hosts on the same port
// are permitted; that is a valid Linux multi-interface setup.
func (c *Config) validateListenAddrs() error {
	addrs := map[string]string{
		"server.http_addr": c.Server.HTTPAddr,
		"server.grpc_addr": c.Server.GRPCAddr,
		"metrics.addr":     c.Metrics.Addr,
	}

	parsed := make(map[string]struct{ host, port string })
	for name, a := range addrs {
		if a == "" {
			continue
		}
		host, port, err := net.SplitHostPort(a)
		if err != nil {
			return fmt.Errorf("%s: invalid address %q: %w", name, a, err)
		}
		if port == "" {
			return fmt.Errorf("%s: address %q must include a port", name, a)
		}
		parsed[name] = struct{ host, port string }{host, port}
	}

	names := make([]string, 0, len(parsed))
	for n := range parsed {
		names = append(names, n)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			a, b := parsed[names[i]], parsed[names[j]]
			if a.port != b.port {
				continue
			}
			if isWildcardHost(a.host) || isWildcardHost(b.host) || a.host == b.host {
				return fmt.Errorf("%s and %s would bind the same socket (%q)",
					names[i], names[j], addrs[names[i]])
			}
		}
	}
	return nil
}

func isWildcardHost(h string) bool {
	return h == "" || h == "0.0.0.0" || h == "::"
}

// ApplyEnvOverrides applies REVERB_* environment variables to the config in
// the same order cmd/reverb does at startup. Callers that mirror the server's
// startup contract (the binary itself, `reverb-cli validate-config`, future
// inspection tools) must run this between YAML load and ApplyDefaults/Validate
// so they see the same effective config the server will. Keeping this on the
// type — alongside ApplyDefaults and Validate — prevents the parity drift
// that previously let `validate-config` accept configs the server rejected.
func (c *Config) ApplyEnvOverrides() {
	if v := os.Getenv("REVERB_DEFAULT_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.DefaultTTL = d
		}
	}
	if v := os.Getenv("REVERB_SIMILARITY_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			c.SimilarityThreshold = float32(f)
		}
	}
	if v := os.Getenv("REVERB_EMBEDDING_API_KEY"); v != "" {
		c.Embedding.APIKey = v
	}
	if v := os.Getenv("REVERB_REDIS_PASSWORD"); v != "" {
		c.Store.RedisPassword = v
	}
	if v := os.Getenv("REVERB_OTEL_ENABLED"); v == "true" || v == "1" {
		c.OTel.Enabled = true
	}
	if v := os.Getenv("REVERB_OTEL_ENDPOINT"); v != "" {
		c.OTel.Endpoint = v
	}
	if v := os.Getenv("REVERB_OTEL_SERVICE_NAME"); v != "" {
		c.OTel.ServiceName = v
	}
	if v := os.Getenv("REVERB_OTEL_INSECURE"); v == "true" || v == "1" {
		c.OTel.Insecure = true
	}
	if v := os.Getenv("REVERB_AUTH_ENABLED"); v == "true" || v == "1" {
		c.Auth.Enabled = true
	}
	if v := os.Getenv("REVERB_AUTH_API_KEY"); v != "" {
		c.Auth.Enabled = true
		c.Auth.Tenants = append(c.Auth.Tenants, Tenant{
			ID: "default", Name: "Default", APIKeys: []string{v},
		})
	}
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
		c.Clock = clock.Real()
	}
}
