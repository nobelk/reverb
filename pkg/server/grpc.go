package server

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/org/reverb/pkg/reverb"
	"github.com/org/reverb/pkg/store"
)

// --- Service description ----------------------------------------------------

// ReverbServiceDesc is the grpc.ServiceDesc for ReverbService.
// It is registered manually so no protoc code generation is needed.
var ReverbServiceDesc = grpc.ServiceDesc{
	ServiceName: "reverb.v1.ReverbService",
	HandlerType: (*ReverbServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Lookup",
			Handler:    _ReverbService_Lookup_Handler,
		},
		{
			MethodName: "Store",
			Handler:    _ReverbService_Store_Handler,
		},
		{
			MethodName: "Invalidate",
			Handler:    _ReverbService_Invalidate_Handler,
		},
		{
			MethodName: "DeleteEntry",
			Handler:    _ReverbService_DeleteEntry_Handler,
		},
		{
			MethodName: "GetStats",
			Handler:    _ReverbService_GetStats_Handler,
		},
	},
	Streams: []grpc.StreamDesc{},
}

// --- Request / response types -----------------------------------------------

// LookupRequest is the input for the Lookup RPC.
type LookupRequest struct {
	Namespace string
	Prompt    string
	ModelID   string
}

// LookupResponse is the output of the Lookup RPC.
type LookupResponse struct {
	Hit        bool
	Tier       string
	Similarity float32
	Entry      *GRPCCacheEntry
}

// GRPCCacheEntry carries a cache entry over gRPC.
type GRPCCacheEntry struct {
	ID           string
	CreatedAtUnix int64
	ExpiresAtUnix int64
	Namespace    string
	Prompt       string
	ModelID      string
	Response     string
	ResponseMeta map[string]string
	Sources      []GRPCSourceRef
	HitCount     int64
}

// GRPCSourceRef carries a source reference over gRPC.
type GRPCSourceRef struct {
	SourceID    string
	ContentHash string
}

// StoreRequest is the input for the Store RPC.
type StoreRequest struct {
	Namespace    string
	Prompt       string
	ModelID      string
	Response     string
	ResponseMeta map[string]string
	Sources      []GRPCSourceRef
	TTLSeconds   int32
}

// StoreResponse is the output of the Store RPC.
type StoreResponse struct {
	ID            string
	CreatedAtUnix int64
}

// InvalidateRequest is the input for the Invalidate RPC.
type InvalidateRequest struct {
	SourceID string
}

// InvalidateResponse is the output of the Invalidate RPC.
type InvalidateResponse struct {
	InvalidatedCount int32
}

// DeleteEntryRequest is the input for the DeleteEntry RPC.
type DeleteEntryRequest struct {
	ID string
}

// DeleteEntryResponse is the output of the DeleteEntry RPC.
type DeleteEntryResponse struct{}

// GetStatsRequest is the input for the GetStats RPC.
type GetStatsRequest struct{}

// GetStatsResponse is the output of the GetStats RPC.
type GetStatsResponse struct {
	TotalEntries       int64
	Namespaces         []string
	ExactHitsTotal     int64
	SemanticHitsTotal  int64
	MissesTotal        int64
	InvalidationsTotal int64
}

// --- Service interface -------------------------------------------------------

// ReverbServiceServer is the interface that must be implemented by a gRPC server.
type ReverbServiceServer interface {
	Lookup(ctx context.Context, req *LookupRequest) (*LookupResponse, error)
	Store(ctx context.Context, req *StoreRequest) (*StoreResponse, error)
	Invalidate(ctx context.Context, req *InvalidateRequest) (*InvalidateResponse, error)
	DeleteEntry(ctx context.Context, req *DeleteEntryRequest) (*DeleteEntryResponse, error)
	GetStats(ctx context.Context, req *GetStatsRequest) (*GetStatsResponse, error)
}

// --- Method handlers --------------------------------------------------------

func _ReverbService_Lookup_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(LookupRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReverbServiceServer).Lookup(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/reverb.v1.ReverbService/Lookup"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReverbServiceServer).Lookup(ctx, req.(*LookupRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ReverbService_Store_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(StoreRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReverbServiceServer).Store(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/reverb.v1.ReverbService/Store"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReverbServiceServer).Store(ctx, req.(*StoreRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ReverbService_Invalidate_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(InvalidateRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReverbServiceServer).Invalidate(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/reverb.v1.ReverbService/Invalidate"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReverbServiceServer).Invalidate(ctx, req.(*InvalidateRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ReverbService_DeleteEntry_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(DeleteEntryRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReverbServiceServer).DeleteEntry(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/reverb.v1.ReverbService/DeleteEntry"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReverbServiceServer).DeleteEntry(ctx, req.(*DeleteEntryRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ReverbService_GetStats_Handler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	in := new(GetStatsRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ReverbServiceServer).GetStats(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/reverb.v1.ReverbService/GetStats"}
	handler := func(ctx context.Context, req any) (any, error) {
		return srv.(ReverbServiceServer).GetStats(ctx, req.(*GetStatsRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// --- GRPCServer -------------------------------------------------------------

// GRPCServer wraps a reverb.Client and exposes it via gRPC.
type GRPCServer struct {
	client *reverb.Client
	server *grpc.Server
}

// NewGRPCServer creates a new GRPCServer wired to the given Reverb client.
func NewGRPCServer(client *reverb.Client, opts ...grpc.ServerOption) *GRPCServer {
	s := &GRPCServer{client: client}
	s.server = grpc.NewServer(opts...)
	s.server.RegisterService(&ReverbServiceDesc, s)
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

func (s *GRPCServer) Lookup(ctx context.Context, req *LookupRequest) (*LookupResponse, error) {
	if req.Namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	if req.Prompt == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}

	result, err := s.client.Lookup(ctx, reverb.LookupRequest{
		Namespace: req.Namespace,
		Prompt:    req.Prompt,
		ModelID:   req.ModelID,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup failed: %v", err)
	}

	resp := &LookupResponse{
		Hit:        result.Hit,
		Tier:       result.Tier,
		Similarity: result.Similarity,
	}
	if result.Entry != nil {
		resp.Entry = toCacheEntryGRPC(result.Entry)
	}
	return resp, nil
}

func (s *GRPCServer) Store(ctx context.Context, req *StoreRequest) (*StoreResponse, error) {
	if req.Namespace == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	if req.Prompt == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}
	if req.Response == "" {
		return nil, status.Error(codes.InvalidArgument, "response is required")
	}

	sources, err := convertGRPCSources(req.Sources)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}

	var ttl time.Duration
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	entry, err := s.client.Store(ctx, reverb.StoreRequest{
		Namespace:    req.Namespace,
		Prompt:       req.Prompt,
		ModelID:      req.ModelID,
		Response:     req.Response,
		ResponseMeta: req.ResponseMeta,
		Sources:      sources,
		TTL:          ttl,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store failed: %v", err)
	}

	return &StoreResponse{
		ID:            entry.ID,
		CreatedAtUnix: entry.CreatedAt.Unix(),
	}, nil
}

func (s *GRPCServer) Invalidate(ctx context.Context, req *InvalidateRequest) (*InvalidateResponse, error) {
	if req.SourceID == "" {
		return nil, status.Error(codes.InvalidArgument, "source_id is required")
	}

	count, err := s.client.Invalidate(ctx, req.SourceID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "invalidate failed: %v", err)
	}

	return &InvalidateResponse{InvalidatedCount: int32(count)}, nil
}

func (s *GRPCServer) DeleteEntry(ctx context.Context, req *DeleteEntryRequest) (*DeleteEntryResponse, error) {
	if req.ID == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	if err := s.client.InvalidateEntry(ctx, req.ID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete entry failed: %v", err)
	}

	return &DeleteEntryResponse{}, nil
}

func (s *GRPCServer) GetStats(ctx context.Context, _ *GetStatsRequest) (*GetStatsResponse, error) {
	stats, err := s.client.Stats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "stats failed: %v", err)
	}

	return &GetStatsResponse{
		TotalEntries:       stats.TotalEntries,
		Namespaces:         stats.Namespaces,
		ExactHitsTotal:     stats.ExactHitsTotal,
		SemanticHitsTotal:  stats.SemanticHitsTotal,
		MissesTotal:        stats.MissesTotal,
		InvalidationsTotal: stats.InvalidationsTotal,
	}, nil
}

// --- helpers ----------------------------------------------------------------

func toCacheEntryGRPC(e *store.CacheEntry) *GRPCCacheEntry {
	sources := make([]GRPCSourceRef, len(e.SourceHashes))
	for i, s := range e.SourceHashes {
		sources[i] = GRPCSourceRef{
			SourceID:    s.SourceID,
			ContentHash: hex.EncodeToString(s.ContentHash[:]),
		}
	}

	return &GRPCCacheEntry{
		ID:            e.ID,
		CreatedAtUnix: e.CreatedAt.Unix(),
		ExpiresAtUnix: e.ExpiresAt.Unix(),
		Namespace:     e.Namespace,
		Prompt:        e.PromptText,
		ModelID:       e.ModelID,
		Response:      e.ResponseText,
		ResponseMeta:  e.ResponseMeta,
		Sources:       sources,
		HitCount:      e.HitCount,
	}
}

func convertGRPCSources(grpcSources []GRPCSourceRef) ([]store.SourceRef, error) {
	if len(grpcSources) == 0 {
		return nil, nil
	}
	refs := make([]store.SourceRef, len(grpcSources))
	for i, gs := range grpcSources {
		refs[i].SourceID = gs.SourceID
		if gs.ContentHash != "" {
			decoded, err := hex.DecodeString(strings.TrimPrefix(gs.ContentHash, "0x"))
			if err != nil {
				return nil, fmt.Errorf("invalid content_hash for source %q: %w", gs.SourceID, err)
			}
			if len(decoded) != 32 {
				return nil, fmt.Errorf("content_hash for source %q must be 32 bytes (got %d)", gs.SourceID, len(decoded))
			}
			copy(refs[i].ContentHash[:], decoded)
		}
	}
	return refs, nil
}
