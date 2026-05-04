package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/nobelk/reverb/pkg/server/proto"
)

// hangingService blocks every RPC until its context expires. Lets us assert
// that the gRPC client applies a per-request deadline rather than waiting on
// the parent context, which is the contract --timeout advertises.
type hangingService struct {
	pb.UnimplementedReverbServiceServer
}

func (hangingService) GetStats(ctx context.Context, _ *pb.GetStatsRequest) (*pb.GetStatsResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func TestGRPCClient_TimeoutAppliesPerRequestDeadline(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterReverbServiceServer(srv, hangingService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(context.Background())
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	c := &grpcClient{
		conn:    conn,
		rpc:     pb.NewReverbServiceClient(conn),
		timeout: 75 * time.Millisecond,
	}

	// Use a Background parent so the only deadline in play is c.timeout.
	// If the client forwards the parent context unchanged (the bug), this
	// call hangs until the test deadline fires.
	start := time.Now()
	_, err = c.Stats(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected DeadlineExceeded, got nil after %s", elapsed)
	}
	if got := status.Code(err); got != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v (err=%v)", got, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("client honored deadline too late: elapsed=%s timeout=%s", elapsed, c.timeout)
	}
}

func TestGRPCClient_ZeroTimeoutDoesNotApplyDeadline(t *testing.T) {
	// Mirrors http.Client{Timeout: 0} semantics: no per-request deadline.
	// Verified by ensuring the parent context (with its own short deadline)
	// is what actually cancels the call.
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterReverbServiceServer(srv, hangingService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(_ context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(context.Background())
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufnet: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	c := &grpcClient{
		conn:    conn,
		rpc:     pb.NewReverbServiceClient(conn),
		timeout: 0,
	}

	parentCtx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	_, err = c.Stats(parentCtx)
	if err == nil || !errors.Is(parentCtx.Err(), context.DeadlineExceeded) {
		t.Fatalf("expected parent context to drive cancellation, err=%v", err)
	}
}
