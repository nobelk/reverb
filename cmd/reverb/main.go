package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"gopkg.in/yaml.v3"

	"github.com/nobelk/reverb/pkg/auth"
	"github.com/nobelk/reverb/pkg/cdc"
	"github.com/nobelk/reverb/pkg/cdc/nats"
	"github.com/nobelk/reverb/pkg/cdc/webhook"
	"github.com/nobelk/reverb/pkg/embedding"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/embedding/ollama"
	"github.com/nobelk/reverb/pkg/embedding/openai"
	"github.com/nobelk/reverb/pkg/embedding/throttled"
	"github.com/nobelk/reverb/pkg/limiter"
	"github.com/nobelk/reverb/pkg/metrics"
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
	validate := flag.Bool("validate", false, "Validate config and construct the engine, then exit. Does not bind listeners or call the embedder. Exits 0 on success, non-zero with a structured JSON error report on failure.")
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

	cfg.ApplyEnvOverrides()
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	if *validate {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := validateEngine(ctx, cfg); err != nil {
			logger.Error("validation failed", "error", err)
			os.Exit(1)
		}
		logger.Info("validation ok",
			"store_backend", cfg.Store.Backend,
			"embedding_provider", cfg.Embedding.Provider,
			"vector_backend", cfg.Vector.Backend,
			"auth_enabled", cfg.Auth.Enabled,
			"cdc_enabled", cfg.CDC.Enabled,
		)
		return
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

	// Build the embedding-pipeline concurrency cap before the Prometheus
	// collector so we can wire metric callbacks once both exist. The
	// throttled wrapper itself is applied after the collector is built so
	// it can record in-flight / queue-depth gauges.
	embedConcurrency := limiter.NewConcurrencyLimiter(
		cfg.Concurrency.MaxInFlight,
		cfg.Concurrency.MaxQueued,
		cfg.Concurrency.MaxQueueWait,
	)
	if embedConcurrency != nil {
		logger.Info("embedding concurrency cap enabled",
			"max_in_flight", cfg.Concurrency.MaxInFlight,
			"max_queued", cfg.Concurrency.MaxQueued,
			"max_queue_wait", cfg.Concurrency.MaxQueueWait,
		)
	}

	vi, err := newVectorIndex(cfg)
	if err != nil {
		logger.Error("failed to create vector index", "error", err)
		os.Exit(1)
	}

	var reverbOpts []reverb.Option

	if cfg.Store.RebuildVectorIndexOnStartup {
		reverbOpts = append(reverbOpts, reverb.WithRebuildVectorIndex(true))
		logger.Info("vector index rebuild on startup enabled", "store", cfg.Store.Backend)
	}

	// Build a dedicated Prometheus registry rather than sharing the default
	// global one. This keeps test isolation predictable and makes it explicit
	// which metrics we serve.
	promRegistry := prometheus.NewRegistry()
	promRegistry.MustRegister(collectors.NewGoCollector())
	promRegistry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	promCollector, err := metrics.NewPrometheusCollector(promRegistry)
	if err != nil {
		logger.Error("failed to register prometheus metrics", "error", err)
		os.Exit(1)
	}
	reverbOpts = append(reverbOpts, reverb.WithPrometheusCollector(promCollector))

	// Wrap the embedder with the concurrency cap once both pieces are
	// available. throttled.New short-circuits when the limiter is nil, so
	// this is safe to call unconditionally.
	embedder = throttled.New(embedder, embedConcurrency, promCollector)

	// Build per-tenant rate limiter from config. NewRegistry returns nil when
	// disabled (or when the configured rate/burst is non-positive); the
	// downstream middleware/interceptor short-circuit on nil so we don't
	// branch here.
	var rateLimitReg *limiter.Registry
	if cfg.RateLimit.Enabled {
		rateLimitReg = limiter.NewRegistry(cfg.RateLimit.RequestsPerSecond, cfg.RateLimit.Burst, nil)
		logger.Info("per-tenant rate limiting enabled",
			"requests_per_second", cfg.RateLimit.RequestsPerSecond,
			"burst", cfg.RateLimit.Burst,
		)
	}

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

	var authn *auth.Authenticator
	if cfg.Auth.Enabled {
		var err error
		authn, err = auth.NewAuthenticator(cfg.Auth)
		if err != nil {
			logger.Error("failed to create authenticator", "error", err)
			os.Exit(1)
		}
		logger.Info("API key authentication enabled", "tenants", len(cfg.Auth.Tenants))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addr := cfg.Server.HTTPAddr
	if addr == "" {
		addr = ":8080"
	}

	httpOpts := []server.HTTPServerOption{
		server.WithReadinessCheck(storeReadiness(s)),
		server.WithRateLimiter(rateLimitReg),
		server.WithMetricsCollector(promCollector),
	}
	// When no dedicated metrics listener is configured, expose /metrics on the
	// main HTTP mux. Auth middleware bypasses /metrics so scrapers don't need
	// credentials — operators wanting a gated endpoint should configure
	// metrics.addr for a separate listener on an internal-only interface.
	if cfg.Metrics.Addr == "" {
		httpOpts = append(httpOpts, server.WithMetricsOnMux(promRegistry))
	}

	srv := server.NewHTTPServer(client, addr, authn, httpOpts...)
	logger.Info("starting HTTP server", "addr", addr, "store", cfg.Store.Backend, "embedder", cfg.Embedding.Provider)

	errCh := make(chan error, 3)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	// Start dedicated metrics server if configured.
	if cfg.Metrics.Addr != "" {
		metricsSrv := server.NewMetricsServer(cfg.Metrics.Addr, promRegistry)
		go func() {
			logger.Info("starting metrics server", "addr", cfg.Metrics.Addr)
			if err := metricsSrv.Start(ctx); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("metrics: %w", err)
			}
		}()
	}

	// Start gRPC server if configured
	if cfg.Server.GRPCAddr != "" {
		grpcSrv := server.NewGRPCServer(client, authn,
			server.WithGRPCRateLimiter(rateLimitReg),
			server.WithGRPCMetricsCollector(promCollector),
		)
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

// storeReadiness returns a ReadinessChecker that probes the store. A Stats
// call exercises connectivity for remote backends (redis) and sanity-checks
// durable ones (badger) without modifying state.
func storeReadiness(s store.Store) server.ReadinessChecker {
	return func(ctx context.Context) error {
		_, err := s.Stats(ctx)
		return err
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

// validateEngine constructs the full engine (store, embedder, vector index,
// reverb client, authenticator) without binding any network listeners. It
// powers the `--validate` flag: an operator can prove the config wires up to
// a working engine before promoting it to production.
//
// Per Phase 1 spec: this path skips the embedder Embed call. A missing API
// key or unreachable embedding endpoint will not be surfaced here. Store
// connectivity *is* exercised via Stats(), so unreachable Redis or a
// malformed Badger directory will fail fast.
func validateEngine(ctx context.Context, cfg reverb.Config) error {
	s, err := newStore(cfg)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	embedder, err := newEmbedder(cfg)
	if err != nil {
		return fmt.Errorf("embedder: %w", err)
	}

	vi, err := newVectorIndex(cfg)
	if err != nil {
		return fmt.Errorf("vector index: %w", err)
	}

	promRegistry := prometheus.NewRegistry()
	promCollector, err := metrics.NewPrometheusCollector(promRegistry)
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}

	client, err := reverb.New(cfg, embedder, s, vi, reverb.WithPrometheusCollector(promCollector))
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}
	defer client.Close()

	if cfg.Auth.Enabled {
		if _, err := auth.NewAuthenticator(cfg.Auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	if _, err := client.Stats(ctx); err != nil {
		return fmt.Errorf("store readiness: %w", err)
	}

	return nil
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
