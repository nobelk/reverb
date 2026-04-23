package reverb_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/reverb"
)

func TestConfig_Validate_Valid(t *testing.T) {
	cfg := reverb.DefaultConfig()
	require.NoError(t, cfg.Validate())
}

func TestConfig_Validate_InvalidThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold float32
	}{
		{"negative", -0.1},
		{"above_one", 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := reverb.DefaultConfig()
			cfg.SimilarityThreshold = tt.threshold
			assert.Error(t, cfg.Validate())
		})
	}
}

func TestConfig_Validate_InvalidTopK(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.SemanticTopK = 0
	assert.Error(t, cfg.Validate())
}

func TestConfig_Validate_NegativeTTL(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.DefaultTTL = -1 * time.Second
	assert.Error(t, cfg.Validate())
}

func TestConfig_ApplyDefaults(t *testing.T) {
	cfg := reverb.Config{}
	cfg.ApplyDefaults()
	assert.Equal(t, "default", cfg.DefaultNamespace)
	assert.Equal(t, 24*time.Hour, cfg.DefaultTTL)
	assert.Equal(t, float32(0.95), cfg.SimilarityThreshold)
	assert.Equal(t, 5, cfg.SemanticTopK)
	assert.Equal(t, "memory", cfg.Store.Backend)
	assert.Equal(t, "flat", cfg.Vector.Backend)
	assert.NotNil(t, cfg.Clock)
}

func TestDefaultConfig_Values(t *testing.T) {
	cfg := reverb.DefaultConfig()
	assert.Equal(t, "default", cfg.DefaultNamespace)
	assert.Equal(t, 24*time.Hour, cfg.DefaultTTL)
	assert.Equal(t, float32(0.95), cfg.SimilarityThreshold)
	assert.Equal(t, 5, cfg.SemanticTopK)
	assert.True(t, cfg.ScopeByModel)
}

func TestConfig_Validate_ListenAddrCollision(t *testing.T) {
	tests := []struct {
		name    string
		http    string
		grpc    string
		metrics string
		wantErr string
	}{
		{"http_grpc_exact_match", ":8080", ":8080", "", "same socket"},
		{"http_metrics_wildcard_explicit", ":9090", "", "0.0.0.0:9090", "same socket"},
		{"grpc_metrics_both_wildcard", "", ":7777", ":7777", "same socket"},
		{"ipv6_wildcard_conflict", "[::]:8080", "", ":8080", "same socket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := reverb.DefaultConfig()
			cfg.Server.HTTPAddr = tt.http
			cfg.Server.GRPCAddr = tt.grpc
			cfg.Metrics.Addr = tt.metrics
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestConfig_Validate_DistinctHostsSamePortAllowed(t *testing.T) {
	// Different explicit hosts on the same port is a valid multi-NIC setup.
	cfg := reverb.DefaultConfig()
	cfg.Server.HTTPAddr = "127.0.0.1:8080"
	cfg.Server.GRPCAddr = "10.0.0.1:8080"
	require.NoError(t, cfg.Validate())
}

func TestConfig_Validate_InvalidAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr string
	}{
		{"no_port", "localhost", "invalid address"},
		{"empty_port", "localhost:", "must include a port"},
		{"garbage", "not-a-real-addr:port:thing", "invalid address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := reverb.DefaultConfig()
			cfg.Server.HTTPAddr = tt.addr
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestConfig_Validate_AuthWithoutListener(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.Auth.Enabled = true
	cfg.Auth.Tenants = []reverb.Tenant{
		{ID: "t1", APIKeys: []string{"k1"}},
	}
	// No HTTP or gRPC listener: auth protects nothing.
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth is enabled")
}

func TestConfig_Validate_AuthWithHTTPOK(t *testing.T) {
	cfg := reverb.DefaultConfig()
	cfg.Auth.Enabled = true
	cfg.Auth.Tenants = []reverb.Tenant{
		{ID: "t1", APIKeys: []string{"k1"}},
	}
	cfg.Server.HTTPAddr = ":8080"
	require.NoError(t, cfg.Validate())
}

func TestConfig_Validate_MetricsOnlyNoCollision(t *testing.T) {
	// Three distinct ports is the happy path.
	cfg := reverb.DefaultConfig()
	cfg.Server.HTTPAddr = ":8080"
	cfg.Server.GRPCAddr = ":9090"
	cfg.Metrics.Addr = ":9100"
	require.NoError(t, cfg.Validate())
}
