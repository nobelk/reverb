package auth

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor returns a gRPC unary interceptor that validates
// Bearer tokens from the "authorization" metadata key and injects tenant
// info into the context.
func UnaryServerInterceptor(authn *Authenticator) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		vals := md.Get("authorization")
		if len(vals) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization metadata")
		}

		token, ok := strings.CutPrefix(vals[0], "Bearer ")
		if !ok || token == "" {
			return nil, status.Error(codes.Unauthenticated, "invalid authorization format")
		}

		tenant, valid := authn.Authenticate(token)
		if !valid {
			return nil, status.Error(codes.Unauthenticated, "invalid API key")
		}

		return handler(WithTenant(ctx, tenant), req)
	}
}
