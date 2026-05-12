package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/nobelk/reverb/pkg/auth"
	"github.com/nobelk/reverb/pkg/limiter"
	"github.com/nobelk/reverb/pkg/metrics"
	"github.com/nobelk/reverb/pkg/reverb"
	pb "github.com/nobelk/reverb/pkg/server/proto"
	"github.com/nobelk/reverb/pkg/store"
)

// GRPCServer wraps a reverb.Client and exposes it via gRPC.
type GRPCServer struct {
	pb.UnimplementedReverbServiceServer
	client      *reverb.Client
	server      *grpc.Server
	rateLimiter *limiter.Registry
	prom        *metrics.PrometheusCollector
}

// GRPCServerOption configures a GRPCServer at construction time.
type GRPCServerOption func(*GRPCServer)

// WithGRPCRateLimiter installs a per-tenant rate-limit interceptor that
// rejects over-rate requests with codes.ResourceExhausted. Pass nil to
// disable.
func WithGRPCRateLimiter(reg *limiter.Registry) GRPCServerOption {
	return func(s *GRPCServer) {
		s.rateLimiter = reg
	}
}

// WithGRPCMetricsCollector wires the Prometheus collector so the rate-limit
// interceptor can record rejection counters.
func WithGRPCMetricsCollector(pc *metrics.PrometheusCollector) GRPCServerOption {
	return func(s *GRPCServer) {
		s.prom = pc
	}
}

// NewGRPCServer creates a new GRPCServer wired to the given Reverb client.
// When authn is non-nil, all RPCs require a valid Bearer token in the
// "authorization" metadata key.
func NewGRPCServer(client *reverb.Client, authn *auth.Authenticator, opts ...GRPCServerOption) *GRPCServer {
	s := &GRPCServer{client: client}
	for _, opt := range opts {
		opt(s)
	}

	// Interceptor order matters: auth runs first so the rate limiter sees
	// the tenant ID. ChainUnaryInterceptor invokes interceptors in the
	// order given.
	var (
		interceptors []grpc.UnaryServerInterceptor
		grpcOpts     []grpc.ServerOption
	)
	if authn != nil {
		interceptors = append(interceptors, auth.UnaryServerInterceptor(authn))
	}
	if s.rateLimiter != nil {
		interceptors = append(interceptors, rateLimitUnaryInterceptor(s.rateLimiter, s.prom))
	}
	if len(interceptors) > 0 {
		grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(interceptors...))
	}

	grpcOpts = append(grpcOpts, grpc.StatsHandler(otelgrpc.NewServerHandler()))
	s.server = grpc.NewServer(grpcOpts...)
	pb.RegisterReverbServiceServer(s.server, s)
	return s
}

// rateLimitUnaryInterceptor enforces a per-tenant token-bucket rate limit on
// every unary RPC. Over-rate requests get codes.ResourceExhausted, which
// gRPC clients already know to retry with backoff.
func rateLimitUnaryInterceptor(reg *limiter.Registry, pc *metrics.PrometheusCollector) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		tenantKey := limiter.AnonymousTenant
		if t, ok := auth.TenantFromContext(ctx); ok {
			tenantKey = t.ID
		}
		if ok, _ := reg.Allow(tenantKey); !ok {
			if pc != nil {
				pc.RejectedRequestsTotal.WithLabelValues("grpc", "rate_limit").Inc()
			}
			return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(ctx, req)
	}
}

// Serve starts the gRPC server on the given listener. It blocks until the
// listener is closed.
func (s *GRPCServer) Serve(lis net.Listener) error {
	return s.server.Serve(lis)
}

// GracefulStop stops the gRPC server gracefully.
func (s *GRPCServer) GracefulStop() {
	s.server.GracefulStop()
}

// Stop stops the gRPC server immediately.
func (s *GRPCServer) Stop() {
	s.server.Stop()
}

// Start begins serving on the given address and blocks until the context is
// cancelled, then performs a graceful stop.
func (s *GRPCServer) Start(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("grpc: listen on %s: %w", addr, err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.server.Serve(lis); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.server.GracefulStop()
		return nil
	}
}

// --- ReverbServiceServer implementation -------------------------------------

// LookupStream replays a cached response as a stream of ResponseChunk
// messages. Misses close with codes.NotFound. Entries that were stored
// without chunks are emitted as a single chunk carrying the full response
// text and finish_reason="stop", mirroring the SSE endpoint's behavior.
func (s *GRPCServer) LookupStream(req *pb.LookupRequest, stream pb.ReverbService_LookupStreamServer) error {
	if req.GetNamespace() == "" {
		return status.Error(codes.InvalidArgument, "namespace is required")
	}
	if req.GetPrompt() == "" {
		return status.Error(codes.InvalidArgument, "prompt is required")
	}

	ctx := stream.Context()
	result, err := s.client.Lookup(ctx, reverb.LookupRequest{
		Namespace: auth.ScopedNamespace(ctx, req.GetNamespace()),
		Prompt:    req.GetPrompt(),
		ModelID:   req.GetModelId(),
	})
	if err != nil {
		return status.Errorf(codes.Internal, "lookup failed: %v", err)
	}
	if !result.Hit {
		return status.Error(codes.NotFound, "no cached response")
	}

	chunks := result.Entry.Chunks
	if len(chunks) == 0 {
		// Legacy entry: synthesize a single terminal chunk.
		chunks = []store.ResponseChunk{{Delta: result.Entry.ResponseText, FinishReason: "stop"}}
	}
	for _, ch := range chunks {
		if err := stream.Send(&pb.ResponseChunk{
			Delta:        ch.Delta,
			FinishReason: ch.FinishReason,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *GRPCServer) Lookup(ctx context.Context, req *pb.LookupRequest) (*pb.LookupResponse, error) {
	if req.GetNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	if req.GetPrompt() == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}

	result, err := s.client.Lookup(ctx, reverb.LookupRequest{
		Namespace: auth.ScopedNamespace(ctx, req.GetNamespace()),
		Prompt:    req.GetPrompt(),
		ModelID:   req.GetModelId(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}

	resp := &pb.LookupResponse{
		Hit:        result.Hit,
		Tier:       result.Tier,
		Similarity: result.Similarity,
	}
	if result.Entry != nil {
		resp.Entry = toCacheEntryProto(result.Entry)
	}
	return resp, nil
}

func (s *GRPCServer) Store(ctx context.Context, req *pb.StoreRequest) (*pb.StoreResponse, error) {
	if req.GetNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	if req.GetPrompt() == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}
	if req.GetResponse() == "" && len(req.GetChunks()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "response or chunks is required")
	}

	sources, err := convertProtoSources(req.GetSources())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	var ttl time.Duration
	if req.GetTtlSeconds() > 0 {
		ttl = time.Duration(req.GetTtlSeconds()) * time.Second
	}

	chunks := convertProtoChunks(req.GetChunks())

	entry, err := s.client.Store(ctx, reverb.StoreRequest{
		Namespace:    auth.ScopedNamespace(ctx, req.GetNamespace()),
		Prompt:       req.GetPrompt(),
		ModelID:      req.GetModelId(),
		Response:     req.GetResponse(),
		Chunks:       chunks,
		ResponseMeta: req.GetResponseMeta(),
		Sources:      sources,
		TTL:          ttl,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store failed: %v", err)
	}

	return &pb.StoreResponse{
		Id:            entry.ID,
		CreatedAtUnix: entry.CreatedAt.Unix(),
	}, nil
}

func (s *GRPCServer) Invalidate(ctx context.Context, req *pb.InvalidateRequest) (*pb.InvalidateResponse, error) {
	if req.GetSourceId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_id is required")
	}

	count, err := s.client.Invalidate(ctx, req.GetSourceId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "invalidate failed: %v", err)
	}

	return &pb.InvalidateResponse{InvalidatedCount: int32(count)}, nil
}

func (s *GRPCServer) DeleteEntry(ctx context.Context, req *pb.DeleteEntryRequest) (*pb.DeleteEntryResponse, error) {
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	// Tenant ownership check: when auth is active, verify the entry's
	// namespace belongs to the requesting tenant before deleting.
	if tenant, ok := auth.TenantFromContext(ctx); ok {
		entry, err := s.client.GetEntry(ctx, req.GetId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "delete entry: get failed: %v", err)
		}
		if entry == nil || !auth.NamespaceBelongsToTenant(tenant.ID, entry.Namespace) {
			return nil, status.Error(codes.NotFound, "entry not found")
		}
	}

	if err := s.client.InvalidateEntry(ctx, req.GetId()); err != nil {
		return nil, status.Errorf(codes.Internal, "delete entry failed: %v", err)
	}

	return &pb.DeleteEntryResponse{}, nil
}

func (s *GRPCServer) GetStats(ctx context.Context, _ *pb.GetStatsRequest) (*pb.GetStatsResponse, error) {
	stats, err := s.client.Stats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stats failed: %v", err)
	}

	namespaces := stats.Namespaces
	totalEntries := stats.TotalEntries
	if tenant, ok := auth.TenantFromContext(ctx); ok {
		var (
			filtered []string
			scoped   []string
		)
		for _, ns := range stats.Namespaces {
			if unscoped, match := auth.UnscopeNamespace(tenant.ID, ns); match {
				filtered = append(filtered, unscoped)
				scoped = append(scoped, ns)
			}
		}
		namespaces = filtered

		var tenantTotal int64
		for _, ns := range scoped {
			n, err := s.client.CountInNamespace(ctx, ns)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "stats: count failed: %v", err)
			}
			tenantTotal += n
		}
		totalEntries = tenantTotal
	}

	return &pb.GetStatsResponse{
		TotalEntries:       totalEntries,
		Namespaces:         namespaces,
		ExactHitsTotal:     stats.ExactHitsTotal,
		SemanticHitsTotal:  stats.SemanticHitsTotal,
		MissesTotal:        stats.MissesTotal,
		InvalidationsTotal: stats.InvalidationsTotal,
	}, nil
}

// --- helpers ----------------------------------------------------------------

func toCacheEntryProto(e *store.CacheEntry) *pb.CacheEntry {
	sources := make([]*pb.SourceRef, len(e.SourceHashes))
	for i, s := range e.SourceHashes {
		sources[i] = &pb.SourceRef{
			SourceId:    s.SourceID,
			ContentHash: hex.EncodeToString(s.ContentHash[:]),
		}
	}

	chunks := make([]*pb.ResponseChunk, len(e.Chunks))
	for i, ch := range e.Chunks {
		chunks[i] = &pb.ResponseChunk{Delta: ch.Delta, FinishReason: ch.FinishReason}
	}

	return &pb.CacheEntry{
		Id:            e.ID,
		CreatedAtUnix: e.CreatedAt.Unix(),
		ExpiresAtUnix: e.ExpiresAt.Unix(),
		Namespace:     e.Namespace,
		Prompt:        e.PromptText,
		ModelId:       e.ModelID,
		Response:      e.ResponseText,
		ResponseMeta:  e.ResponseMeta,
		Sources:       sources,
		HitCount:      e.HitCount,
		Chunks:        chunks,
	}
}

func convertProtoChunks(pbChunks []*pb.ResponseChunk) []store.ResponseChunk {
	if len(pbChunks) == 0 {
		return nil
	}
	out := make([]store.ResponseChunk, len(pbChunks))
	for i, c := range pbChunks {
		out[i] = store.ResponseChunk{Delta: c.GetDelta(), FinishReason: c.GetFinishReason()}
	}
	return out
}

func convertProtoSources(pbSources []*pb.SourceRef) ([]store.SourceRef, error) {
	if len(pbSources) == 0 {
		return nil, nil
	}
	refs := make([]store.SourceRef, len(pbSources))
	for i, ps := range pbSources {
		refs[i].SourceID = ps.GetSourceId()
		if ps.GetContentHash() != "" {
			decoded, err := hex.DecodeString(strings.TrimPrefix(ps.GetContentHash(), "0x"))
			if err != nil {
				return nil, fmt.Errorf("invalid content_hash for source %q: %w", ps.GetSourceId(), err)
			}
			if len(decoded) != 32 {
				return nil, fmt.Errorf("content_hash for source %q must be 32 bytes (got %d)", ps.GetSourceId(), len(decoded))
			}
			copy(refs[i].ContentHash[:], decoded)
		}
	}
	return refs, nil
}
