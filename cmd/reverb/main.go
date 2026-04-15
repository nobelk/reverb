package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"gopkg.in/yaml.v3"

	"github.com/nobelk/reverb/pkg/cdc"
	"github.com/nobelk/reverb/pkg/cdc/nats"
	"github.com/nobelk/reverb/pkg/cdc/webhook"
	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/embedding/ollama"
	"github.com/nobelk/reverb/pkg/embedding/openai"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	badgerstore "github.com/nobelk/reverb/pkg/store/badger"
	"github.com/nobelk/reverb/pkg/store"
	"github.com/nobelk/reverb/pkg/store/memory"
	redistore "github.com/nobelk/reverb/pkg/store/redis"
	"github.com/nobelk/reverb/pkg/vector"
	"github.com/nobelk/reverb/pkg/vector/flat"
	"github.com/nobelk/reverb/pkg/vector/hnsw"
)

func main() {
	httpAddr := flag.String("http-addr", ":8080", "HTTP listen address")
	configPath := flag.String("config", "", "Path to YAML config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := reverb.DefaultConfig()

	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			logger.Error("failed to read config file", "path", *configPath, "error", err)
			os.Exit(1)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			logger.Error("failed to parse config file", "error", err)
			os.Exit(1)
		}
	}

	applyEnvOverrides(&cfg)
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	// Initialize OpenTelemetry tracing
	shutdownTracer, err := initTracer(context.Background(), cfg)
	if err != nil {
		logger.Error("failed to initialize OTel tracer", "error", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracer(shutdownCtx); err != nil {
			logger.Error("failed to shut down OTel tracer", "error", err)
		}
	}()

	// Override HTTP addr from flag if explicitly set
	if *httpAddr != ":8080" || cfg.Server.HTTPAddr == "" {
		cfg.Server.HTTPAddr = *httpAddr
	}

	s, err := newStore(cfg)
	if err != nil {
		logger.Error("failed to create store", "error", err)
		os.Exit(1)
	}

	embedder, err := newEmbedder(cfg)
	if err != nil {
		logger.Error("failed to create embedder", "error", err)
		os.Exit(1)
	}

	vi, err := newVectorIndex(cfg)
	if err != nil {
		logger.Error("failed to create vector index", "error", err)
		os.Exit(1)
	}

	var reverbOpts []reverb.Option

	// Start CDC listener if enabled
	if cfg.CDC.Enabled {
		listener, err := newCDCListener(cfg)
		if err != nil {
			logger.Error("failed to create CDC listener", "error", err)
			os.Exit(1)
		}
		if listener != nil {
			reverbOpts = append(reverbOpts, reverb.WithCDCListener(listener))
			logger.Info("CDC listener configured", "mode", cfg.CDC.Mode)
		}
	}

	client, err := reverb.New(cfg, embedder, s, vi, reverbOpts...)
	if err != nil {
		logger.Error("failed to create reverb client", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := cfg.Server.HTTPAddr
	if addr == "" {
		addr = ":8080"
	}
	srv := server.NewHTTPServer(client, addr)
	logger.Info("starting HTTP server", "addr", addr, "store", cfg.Store.Backend, "embedder", cfg.Embedding.Provider)

	errCh := make(chan error, 2)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Start gRPC server if configured
	if cfg.Server.GRPCAddr != "" {
		grpcSrv := server.NewGRPCServer(client)
		go func() {
			logger.Info("starting gRPC server", "addr", cfg.Server.GRPCAddr)
			if err := grpcSrv.Start(ctx, cfg.Server.GRPCAddr); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("grpc: %w", err)
			}
		}()
	}

	if err := <-errCh; err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// applyEnvOverrides applies environment variable overrides to cfg.
func applyEnvOverrides(cfg *reverb.Config) {
	if v := os.Getenv("REVERB_DEFAULT_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DefaultTTL = d
		}
	}
	if v := os.Getenv("REVERB_SIMILARITY_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 32); err == nil {
			cfg.SimilarityThreshold = float32(f)
		}
	}
	if v := os.Getenv("REVERB_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := os.Getenv("REVERB_REDIS_PASSWORD"); v != "" {
		cfg.Store.RedisPassword = v
	}
	if v := os.Getenv("REVERB_OTEL_ENABLED"); v == "true" || v == "1" {
		cfg.OTel.Enabled = true
	}
	if v := os.Getenv("REVERB_OTEL_ENDPOINT"); v != "" {
		cfg.OTel.Endpoint = v
	}
	if v := os.Getenv("REVERB_OTEL_SERVICE_NAME"); v != "" {
		cfg.OTel.ServiceName = v
	}
	if v := os.Getenv("REVERB_OTEL_INSECURE"); v == "true" || v == "1" {
		cfg.OTel.Insecure = true
	}
}

// newStore creates a store.Store based on cfg.Store.Backend.
func newStore(cfg reverb.Config) (store.Store, error) {
	switch cfg.Store.Backend {
	case "badger":
		path := cfg.Store.BadgerPath
		if path == "" {
			path = "/tmp/reverb-badger"
		}
		return badgerstore.New(path)
	case "redis":
		addr := cfg.Store.RedisAddr
		if addr == "" {
			addr = "localhost:6379"
		}
		prefix := cfg.Store.RedisPrefix
		if prefix == "" {
			prefix = "reverb:"
		}
		return redistore.New(addr, cfg.Store.RedisPassword, cfg.Store.RedisDB, prefix)
	default:
		return memory.New(), nil
	}
}

// newEmbedder creates an embedding.Provider based on cfg.Embedding.Provider.
func newEmbedder(cfg reverb.Config) (embedding.Provider, error) {
	dims := cfg.Embedding.Dimensions
	if dims == 0 {
		dims = 1536
	}
	switch cfg.Embedding.Provider {
	case "openai":
		return openai.New(openai.Config{
			APIKey:     cfg.Embedding.APIKey,
			Model:      cfg.Embedding.Model,
			BaseURL:    cfg.Embedding.BaseURL,
			Dimensions: dims,
		}), nil
	case "ollama":
		baseURL := cfg.Embedding.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		model := cfg.Embedding.Model
		if model == "" {
			model = "nomic-embed-text"
		}
		return ollama.New(baseURL, model), nil
	default:
		return fake.New(dims), nil
	}
}

// newVectorIndex creates a vector.Index based on cfg.Vector.Backend.
func newVectorIndex(cfg reverb.Config) (vector.Index, error) {
	switch cfg.Vector.Backend {
	case "hnsw":
		return hnsw.New(hnsw.Config{
			M:              cfg.Vector.HNSWm,
			EfConstruction: cfg.Vector.HNSWefConstruct,
			EfSearch:       cfg.Vector.HNSWefSearch,
		}, 0), nil
	default:
		return flat.New(0), nil
	}
}

// initTracer sets up the OpenTelemetry TracerProvider with an OTLP HTTP exporter.
// Returns a shutdown function that flushes remaining spans.
func initTracer(ctx context.Context, cfg reverb.Config) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !cfg.OTel.Enabled {
		return noop, nil
	}

	opts := []otlptracehttp.Option{}
	if cfg.OTel.Endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpoint(cfg.OTel.Endpoint))
	}
	if cfg.OTel.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return noop, fmt.Errorf("otel: create exporter: %w", err)
	}

	serviceName := cfg.OTel.ServiceName
	if serviceName == "" {
		serviceName = "reverb"
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return noop, fmt.Errorf("otel: create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// newCDCListener creates a cdc.Listener based on cfg.CDC.Mode.
// Returns nil, nil if CDC is not configured.
func newCDCListener(cfg reverb.Config) (cdc.Listener, error) {
	switch cfg.CDC.Mode {
	case "nats":
		url := cfg.CDC.NatsURL
		if url == "" {
			url = "nats://localhost:4222"
		}
		subject := cfg.CDC.NatsSubject
		if subject == "" {
			subject = "reverb.changes"
		}
		return nats.New(url, subject, nil)
	case "webhook":
		addr := cfg.CDC.WebhookAddr
		if addr == "" {
			addr = ":9091"
		}
		path := cfg.CDC.WebhookPath
		if path == "" {
			path = "/hooks/source-changed"
		}
		return webhook.New(webhook.Config{
			Addr: addr,
			Path: path,
		}), nil
	default:
		return nil, nil
	}
}
