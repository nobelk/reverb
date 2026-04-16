package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/nobelk/reverb/pkg/reverb"
)

func grpcTestAuthn(t *testing.T) *Authenticator {
	t.Helper()
	authn, err := NewAuthenticator(reverb.AuthConfig{
		Tenants: []reverb.Tenant{
			{ID: "t1", Name: "Tenant One", APIKeys: []string{"grpc-key"}},
		},
	})
	require.NoError(t, err)
	return authn
}

// echoInfo is a dummy gRPC unary server info.
var echoInfo = &grpc.UnaryServerInfo{FullMethod: "/test.Service/Echo"}

// passHandler is a dummy handler that returns the tenant ID from context.
func passHandler(ctx context.Context, _ any) (any, error) {
	tenant, ok := TenantFromContext(ctx)
	if !ok {
		return "no-tenant", nil
	}
	return tenant.ID, nil
}

func TestGRPCInterceptor_ValidToken(t *testing.T) {
	interceptor := UnaryServerInterceptor(grpcTestAuthn(t))

	md := metadata.Pairs("authorization", "Bearer grpc-key")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor(ctx, nil, echoInfo, passHandler)
	require.NoError(t, err)
	assert.Equal(t, "t1", resp)
}

func TestGRPCInterceptor_InvalidToken(t *testing.T) {
	interceptor := UnaryServerInterceptor(grpcTestAuthn(t))

	md := metadata.Pairs("authorization", "Bearer wrong")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, echoInfo, passHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestGRPCInterceptor_MissingMetadata(t *testing.T) {
	interceptor := UnaryServerInterceptor(grpcTestAuthn(t))

	_, err := interceptor(context.Background(), nil, echoInfo, passHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestGRPCInterceptor_MissingAuthKey(t *testing.T) {
	interceptor := UnaryServerInterceptor(grpcTestAuthn(t))

	md := metadata.Pairs("other-key", "value")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, echoInfo, passHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestGRPCInterceptor_MalformedAuth(t *testing.T) {
	interceptor := UnaryServerInterceptor(grpcTestAuthn(t))

	md := metadata.Pairs("authorization", "Basic abc")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor(ctx, nil, echoInfo, passHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
