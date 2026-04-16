package server_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/nobelk/reverb/pkg/auth"
	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	pb "github.com/nobelk/reverb/pkg/server/proto"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

const bufSize = 1 << 20 // 1 MB

// setupGRPCServer starts an in-memory gRPC server and returns a connected
// client-side GRPCServer and a cleanup function.
func setupGRPCServer(t *testing.T) (*server.GRPCServer, *grpc.ClientConn) {
	t.Helper()

	s := memory.New()
	vi := flat.New(0)
	embedder := fake.New(64)
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	client, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)

	grpcSrv := server.NewGRPCServer(client, nil)

	lis := bufconn.Listen(bufSize)
	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			// server stopped, ignore
		}
	}()

	t.Cleanup(func() {
		grpcSrv.Stop()
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return grpcSrv, conn
}

// callStore is a helper that stores an entry via the gRPC server directly.
func callStore(t *testing.T, grpcSrv *server.GRPCServer, req *pb.StoreRequest) *pb.StoreResponse {
	t.Helper()
	resp, err := grpcSrv.Store(context.Background(), req)
	require.NoError(t, err)
	return resp
}

func TestGRPC_Lookup_Hit(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	// Store first.
	callStore(t, grpcSrv, &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "Go is a language.",
	})

	// Lookup the same prompt.
	resp, err := grpcSrv.Lookup(context.Background(), &pb.LookupRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.GetHit())
	assert.NotEmpty(t, resp.GetTier())
	assert.NotNil(t, resp.GetEntry())
}

func TestGRPC_Lookup_Miss(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	resp, err := grpcSrv.Lookup(context.Background(), &pb.LookupRequest{
		Namespace: "ns",
		Prompt:    "never stored",
		ModelId:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.GetHit())
	assert.Nil(t, resp.GetEntry())
}

func TestGRPC_Lookup_MissingNamespace(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	_, err := grpcSrv.Lookup(context.Background(), &pb.LookupRequest{
		Prompt:  "hello",
		ModelId: "gpt-4",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace")
}

func TestGRPC_Lookup_MissingPrompt(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	_, err := grpcSrv.Lookup(context.Background(), &pb.LookupRequest{
		Namespace: "ns",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt")
}

func TestGRPC_Store_Success(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	resp, err := grpcSrv.Store(context.Background(), &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "Go is a language.",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetId())
	assert.NotZero(t, resp.GetCreatedAtUnix())
}

func TestGRPC_Store_MissingFields(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	tests := []struct {
		name    string
		req     *pb.StoreRequest
		wantErr string
	}{
		{
			name:    "missing namespace",
			req:     &pb.StoreRequest{Prompt: "p", Response: "r"},
			wantErr: "namespace",
		},
		{
			name:    "missing prompt",
			req:     &pb.StoreRequest{Namespace: "ns", Response: "r"},
			wantErr: "prompt",
		},
		{
			name:    "missing response",
			req:     &pb.StoreRequest{Namespace: "ns", Prompt: "p"},
			wantErr: "response",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := grpcSrv.Store(context.Background(), tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestGRPC_Invalidate_Success(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	// Store with a source.
	callStore(t, grpcSrv, &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "Go is a language.",
		Sources: []*pb.SourceRef{
			{
				SourceId:    "doc-1",
				ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
	})

	resp, err := grpcSrv.Invalidate(context.Background(), &pb.InvalidateRequest{
		SourceId: "doc-1",
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, resp.GetInvalidatedCount(), int32(1))
}

func TestGRPC_Invalidate_MissingSourceID(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	_, err := grpcSrv.Invalidate(context.Background(), &pb.InvalidateRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source_id")
}

func TestGRPC_DeleteEntry_Success(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	storeResp := callStore(t, grpcSrv, &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "Go is a language.",
	})

	_, err := grpcSrv.DeleteEntry(context.Background(), &pb.DeleteEntryRequest{
		Id: storeResp.GetId(),
	})
	require.NoError(t, err)
}

func TestGRPC_DeleteEntry_MissingID(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	_, err := grpcSrv.DeleteEntry(context.Background(), &pb.DeleteEntryRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

func TestGRPC_GetStats(t *testing.T) {
	grpcSrv, _ := setupGRPCServer(t)

	// Store something to ensure non-trivial stats.
	callStore(t, grpcSrv, &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "Go is a language.",
	})

	resp, err := grpcSrv.GetStats(context.Background(), &pb.GetStatsRequest{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, resp.GetTotalEntries(), int64(1))
}

// --- auth tests --------------------------------------------------------------

func setupAuthGRPCServer(t *testing.T) pb.ReverbServiceClient {
	t.Helper()

	s := memory.New()
	vi := flat.New(0)
	embedder := fake.New(64)
	cfg := reverb.Config{
		DefaultTTL:          24 * time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	client, err := reverb.New(cfg, embedder, s, vi)
	require.NoError(t, err)

	authn, err := auth.NewAuthenticator(reverb.AuthConfig{
		Tenants: []reverb.Tenant{
			{ID: "tenant-a", APIKeys: []string{"grpc-key-a"}},
			{ID: "tenant-b", APIKeys: []string{"grpc-key-b"}},
		},
	})
	require.NoError(t, err)

	grpcSrv := server.NewGRPCServer(client, authn)

	lis := bufconn.Listen(bufSize)
	go func() {
		_ = grpcSrv.Serve(lis)
	}()
	t.Cleanup(func() { grpcSrv.Stop() })

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return pb.NewReverbServiceClient(conn)
}

func authedCtx(key string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+key)
	return metadata.NewOutgoingContext(context.Background(), md)
}

func TestGRPC_Auth_Unauthenticated(t *testing.T) {
	client := setupAuthGRPCServer(t)

	_, err := client.Lookup(context.Background(), &pb.LookupRequest{
		Namespace: "ns", Prompt: "hello",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestGRPC_Auth_InvalidKey(t *testing.T) {
	client := setupAuthGRPCServer(t)

	_, err := client.Lookup(authedCtx("wrong-key"), &pb.LookupRequest{
		Namespace: "ns", Prompt: "hello",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestGRPC_Auth_TenantIsolation(t *testing.T) {
	client := setupAuthGRPCServer(t)

	// Tenant A stores an entry.
	_, err := client.Store(authedCtx("grpc-key-a"), &pb.StoreRequest{
		Namespace: "shared-ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "A language by Google.",
	})
	require.NoError(t, err)

	// Tenant A can look it up.
	resp, err := client.Lookup(authedCtx("grpc-key-a"), &pb.LookupRequest{
		Namespace: "shared-ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, resp.GetHit())

	// Tenant B cannot see it.
	resp, err = client.Lookup(authedCtx("grpc-key-b"), &pb.LookupRequest{
		Namespace: "shared-ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
	})
	require.NoError(t, err)
	assert.False(t, resp.GetHit())
}

func TestGRPC_Auth_DeleteEntry_TenantOwnership(t *testing.T) {
	client := setupAuthGRPCServer(t)

	// Tenant A stores an entry.
	storeResp, err := client.Store(authedCtx("grpc-key-a"), &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "What is Go?",
		ModelId:   "gpt-4",
		Response:  "A language.",
	})
	require.NoError(t, err)

	// Tenant B tries to delete → NotFound.
	_, err = client.DeleteEntry(authedCtx("grpc-key-b"), &pb.DeleteEntryRequest{
		Id: storeResp.GetId(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))

	// Tenant A still sees the entry.
	lookupResp, err := client.Lookup(authedCtx("grpc-key-a"), &pb.LookupRequest{
		Namespace: "ns", Prompt: "What is Go?", ModelId: "gpt-4",
	})
	require.NoError(t, err)
	assert.True(t, lookupResp.GetHit())

	// Tenant A can delete.
	_, err = client.DeleteEntry(authedCtx("grpc-key-a"), &pb.DeleteEntryRequest{
		Id: storeResp.GetId(),
	})
	require.NoError(t, err)
}

func TestGRPC_Auth_DeleteEntry_NotFound(t *testing.T) {
	client := setupAuthGRPCServer(t)

	_, err := client.DeleteEntry(authedCtx("grpc-key-a"), &pb.DeleteEntryRequest{
		Id: "nonexistent-id",
	})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestGRPC_Auth_Stats_TotalEntriesScoped(t *testing.T) {
	client := setupAuthGRPCServer(t)

	// Tenant A: 2 entries.
	for _, p := range []string{"prompt-1", "prompt-2"} {
		_, err := client.Store(authedCtx("grpc-key-a"), &pb.StoreRequest{
			Namespace: "ns-a", Prompt: p, ModelId: "gpt-4", Response: "r",
		})
		require.NoError(t, err)
	}
	// Tenant B: 1 entry.
	_, err := client.Store(authedCtx("grpc-key-b"), &pb.StoreRequest{
		Namespace: "ns-b", Prompt: "x", ModelId: "gpt-4", Response: "r",
	})
	require.NoError(t, err)

	statsA, err := client.GetStats(authedCtx("grpc-key-a"), &pb.GetStatsRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, statsA.GetTotalEntries())
	assert.ElementsMatch(t, []string{"ns-a"}, statsA.GetNamespaces())

	statsB, err := client.GetStats(authedCtx("grpc-key-b"), &pb.GetStatsRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, statsB.GetTotalEntries())
	assert.ElementsMatch(t, []string{"ns-b"}, statsB.GetNamespaces())
}

func TestGRPC_ServiceDesc(t *testing.T) {
	assert.Equal(t, "reverb.v1.ReverbService", pb.ReverbService_ServiceDesc.ServiceName)
	assert.Len(t, pb.ReverbService_ServiceDesc.Methods, 5)
}
