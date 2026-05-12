package server_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/nobelk/reverb/pkg/server/proto"
)

// TestGRPC_LookupStream_Hit stores a chunked entry and replays it.
func TestGRPC_LookupStream_Hit(t *testing.T) {
	grpcSrv, conn := setupGRPCServer(t)

	callStore(t, grpcSrv, &pb.StoreRequest{
		Namespace: "ns",
		Prompt:    "stream me",
		ModelId:   "gpt-4",
		Chunks: []*pb.ResponseChunk{
			{Delta: "Hello"},
			{Delta: ", "},
			{Delta: "world!", FinishReason: "stop"},
		},
	})

	client := pb.NewReverbServiceClient(conn)
	stream, err := client.LookupStream(context.Background(), &pb.LookupRequest{
		Namespace: "ns", Prompt: "stream me", ModelId: "gpt-4",
	}, grpc.WaitForReady(true))
	require.NoError(t, err)

	var deltas []string
	for {
		ch, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		deltas = append(deltas, ch.GetDelta())
	}
	assert.Equal(t, []string{"Hello", ", ", "world!"}, deltas)
}

// TestGRPC_LookupStream_Miss closes with NotFound.
func TestGRPC_LookupStream_Miss(t *testing.T) {
	_, conn := setupGRPCServer(t)
	client := pb.NewReverbServiceClient(conn)
	stream, err := client.LookupStream(context.Background(), &pb.LookupRequest{
		Namespace: "ns", Prompt: "nope", ModelId: "gpt-4",
	}, grpc.WaitForReady(true))
	require.NoError(t, err)

	_, recvErr := stream.Recv()
	require.Error(t, recvErr)
	st, ok := status.FromError(recvErr)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}
