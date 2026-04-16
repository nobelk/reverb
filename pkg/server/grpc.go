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
	"github.com/nobelk/reverb/pkg/reverb"
	pb "github.com/nobelk/reverb/pkg/server/proto"
	"github.com/nobelk/reverb/pkg/store"
)

// GRPCServer wraps a reverb.Client and exposes it via gRPC.
type GRPCServer struct {
	pb.UnimplementedReverbServiceServer
	client *reverb.Client
	server *grpc.Server
}

// NewGRPCServer creates a new GRPCServer wired to the given Reverb client.
// When authn is non-nil, all RPCs require a valid Bearer token in the
// "authorization" metadata key.
func NewGRPCServer(client *reverb.Client, authn *auth.Authenticator, opts ...grpc.ServerOption) *GRPCServer {
	s := &GRPCServer{client: client}
	if authn != nil {
		opts = append(opts, grpc.ChainUnaryInterceptor(auth.UnaryServerInterceptor(authn)))
	}
	opts = append(opts, grpc.StatsHandler(otelgrpc.NewServerHandler()))
	s.server = grpc.NewServer(opts...)
	pb.RegisterReverbServiceServer(s.server, s)
	return s
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
	if req.GetResponse() == "" {
		return nil, status.Error(codes.InvalidArgument, "response is required")
	}

	sources, err := convertProtoSources(req.GetSources())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	var ttl time.Duration
	if req.GetTtlSeconds() > 0 {
		ttl = time.Duration(req.GetTtlSeconds()) * time.Second
	}

	entry, err := s.client.Store(ctx, reverb.StoreRequest{
		Namespace:    auth.ScopedNamespace(ctx, req.GetNamespace()),
		Prompt:       req.GetPrompt(),
		ModelID:      req.GetModelId(),
		Response:     req.GetResponse(),
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
	}
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
