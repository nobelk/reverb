package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/nobelk/reverb/pkg/server/proto"
)

// wireClient is the transport-agnostic surface every subcommand uses to
// reach the server. HTTP and gRPC implementations live alongside.
type wireClient interface {
	Stats(ctx context.Context) (*statsResp, error)
	Lookup(ctx context.Context, req *lookupReq) (*lookupResp, error)
	Store(ctx context.Context, req *storeReq) (*storeResp, error)
	Invalidate(ctx context.Context, sourceID string) (*invalidateResp, error)
	Close() error
}

// SourceRef pairs a source identifier with its content hash. ContentHash is
// hex-encoded on the wire over both transports.
type sourceRef struct {
	SourceID    string `json:"source_id"`
	ContentHash string `json:"content_hash"`
}

type cacheEntry struct {
	ID           string            `json:"id"`
	CreatedAt    time.Time         `json:"created_at"`
	ExpiresAt    time.Time         `json:"expires_at,omitzero"`
	Namespace    string            `json:"namespace"`
	Prompt       string            `json:"prompt"`
	ModelID      string            `json:"model_id"`
	Response     string            `json:"response"`
	ResponseMeta map[string]string `json:"response_meta,omitempty"`
	Sources      []sourceRef       `json:"sources,omitempty"`
	HitCount     int64             `json:"hit_count"`
}

type lookupReq struct {
	Namespace string `json:"namespace"`
	Prompt    string `json:"prompt"`
	ModelID   string `json:"model_id,omitempty"`
}

type lookupResp struct {
	Hit        bool        `json:"hit"`
	Tier       string      `json:"tier,omitempty"`
	Similarity float32     `json:"similarity,omitempty"`
	Entry      *cacheEntry `json:"entry,omitempty"`
}

type storeReq struct {
	Namespace    string            `json:"namespace"`
	Prompt       string            `json:"prompt"`
	ModelID      string            `json:"model_id,omitempty"`
	Response     string            `json:"response"`
	ResponseMeta map[string]string `json:"response_meta,omitempty"`
	Sources      []sourceRef       `json:"sources,omitempty"`
	TTLSeconds   int               `json:"ttl_seconds,omitempty"`
}

type storeResp struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type invalidateResp struct {
	InvalidatedCount int `json:"invalidated_count"`
}

type statsResp struct {
	TotalEntries       int64    `json:"total_entries"`
	Namespaces         []string `json:"namespaces"`
	ExactHitsTotal     int64    `json:"exact_hits_total"`
	SemanticHitsTotal  int64    `json:"semantic_hits_total"`
	MissesTotal        int64    `json:"misses_total"`
	InvalidationsTotal int64    `json:"invalidations_total"`
	HitRate            float64  `json:"hit_rate"`
}

// defaultNewClient is the production wireClient builder. Tests substitute
// this on env.newClient to direct calls at a fake.
func defaultNewClient(e *env) (wireClient, error) {
	timeout, err := time.ParseDuration(e.timeout)
	if err != nil {
		return nil, fmt.Errorf("invalid --timeout %q: %w", e.timeout, err)
	}
	switch strings.ToLower(e.transport) {
	case "", "http":
		return newHTTPClient(e.server, e.token, timeout)
	case "grpc":
		return newGRPCClient(e.server, e.token, timeout)
	default:
		return nil, fmt.Errorf("unknown --transport %q (want http or grpc)", e.transport)
	}
}

// --- HTTP client -----------------------------------------------------------

type httpClient struct {
	base    string
	token   string
	timeout time.Duration
	cli     *http.Client
}

func newHTTPClient(server, token string, timeout time.Duration) (*httpClient, error) {
	base := strings.TrimRight(server, "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &httpClient{
		base:    base,
		token:   token,
		timeout: timeout,
		cli:     &http.Client{Timeout: timeout},
	}, nil
}

func (c *httpClient) Close() error { return nil }

func (c *httpClient) do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.cli.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var er struct {
			Error string `json:"error"`
		}
		// Decode best-effort: a non-JSON body yields the empty Error
		// field, which we substitute with the status text below.
		_ = json.NewDecoder(resp.Body).Decode(&er)
		msg := er.Error
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *httpClient) Stats(ctx context.Context) (*statsResp, error) {
	var out statsResp
	if err := c.do(ctx, http.MethodGet, "/v1/stats", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) Lookup(ctx context.Context, req *lookupReq) (*lookupResp, error) {
	var out lookupResp
	if err := c.do(ctx, http.MethodPost, "/v1/lookup", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) Store(ctx context.Context, req *storeReq) (*storeResp, error) {
	var out storeResp
	if err := c.do(ctx, http.MethodPost, "/v1/store", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpClient) Invalidate(ctx context.Context, sourceID string) (*invalidateResp, error) {
	var out invalidateResp
	body := map[string]string{"source_id": sourceID}
	if err := c.do(ctx, http.MethodPost, "/v1/invalidate", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- gRPC client -----------------------------------------------------------

type grpcClient struct {
	conn    *grpc.ClientConn
	rpc     pb.ReverbServiceClient
	token   string
	timeout time.Duration
}

func newGRPCClient(server, token string, timeout time.Duration) (*grpcClient, error) {
	target := strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://")
	// The CLI defaults to insecure transport — operators terminate TLS at
	// a sidecar or load balancer. Pulling in TLS materials here would
	// expand the dependency footprint without a corresponding use case;
	// add an opt-in flag if/when an operator needs it.
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial grpc %s: %w", target, err)
	}
	return &grpcClient{
		conn:    conn,
		rpc:     pb.NewReverbServiceClient(conn),
		token:   token,
		timeout: timeout,
	}, nil
}

func (c *grpcClient) Close() error { return c.conn.Close() }

// callCtx attaches the bearer token (if any) and the per-request deadline to
// ctx. A zero timeout disables the deadline so it mirrors http.Client{Timeout:
// 0} semantics on the HTTP path. Callers must invoke the returned cancel.
func (c *grpcClient) callCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.token != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.token)
	}
	if c.timeout > 0 {
		return context.WithTimeout(ctx, c.timeout)
	}
	return ctx, func() {}
}

func (c *grpcClient) Stats(ctx context.Context) (*statsResp, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.GetStats(ctx, &pb.GetStatsRequest{})
	if err != nil {
		return nil, err
	}
	// HitRate is server-derived in HTTP but not present on the proto
	// response — recompute locally so output is identical across
	// transports.
	out := &statsResp{
		TotalEntries:       resp.GetTotalEntries(),
		Namespaces:         resp.GetNamespaces(),
		ExactHitsTotal:     resp.GetExactHitsTotal(),
		SemanticHitsTotal:  resp.GetSemanticHitsTotal(),
		MissesTotal:        resp.GetMissesTotal(),
		InvalidationsTotal: resp.GetInvalidationsTotal(),
	}
	if total := out.ExactHitsTotal + out.SemanticHitsTotal + out.MissesTotal; total > 0 {
		out.HitRate = float64(out.ExactHitsTotal+out.SemanticHitsTotal) / float64(total)
	}
	return out, nil
}

func (c *grpcClient) Lookup(ctx context.Context, req *lookupReq) (*lookupResp, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.Lookup(ctx, &pb.LookupRequest{
		Namespace: req.Namespace,
		Prompt:    req.Prompt,
		ModelId:   req.ModelID,
	})
	if err != nil {
		return nil, err
	}
	out := &lookupResp{
		Hit:        resp.GetHit(),
		Tier:       resp.GetTier(),
		Similarity: resp.GetSimilarity(),
	}
	if pe := resp.GetEntry(); pe != nil {
		out.Entry = protoToCacheEntry(pe)
	}
	return out, nil
}

func (c *grpcClient) Store(ctx context.Context, req *storeReq) (*storeResp, error) {
	srcs, err := stringSourcesToProto(req.Sources)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.Store(ctx, &pb.StoreRequest{
		Namespace:    req.Namespace,
		Prompt:       req.Prompt,
		ModelId:      req.ModelID,
		Response:     req.Response,
		ResponseMeta: req.ResponseMeta,
		Sources:      srcs,
		TtlSeconds:   int32(req.TTLSeconds),
	})
	if err != nil {
		return nil, err
	}
	return &storeResp{
		ID:        resp.GetId(),
		CreatedAt: time.Unix(resp.GetCreatedAtUnix(), 0).UTC(),
	}, nil
}

func (c *grpcClient) Invalidate(ctx context.Context, sourceID string) (*invalidateResp, error) {
	ctx, cancel := c.callCtx(ctx)
	defer cancel()
	resp, err := c.rpc.Invalidate(ctx, &pb.InvalidateRequest{SourceId: sourceID})
	if err != nil {
		return nil, err
	}
	return &invalidateResp{InvalidatedCount: int(resp.GetInvalidatedCount())}, nil
}

func protoToCacheEntry(pe *pb.CacheEntry) *cacheEntry {
	out := &cacheEntry{
		ID:           pe.GetId(),
		Namespace:    pe.GetNamespace(),
		Prompt:       pe.GetPrompt(),
		ModelID:      pe.GetModelId(),
		Response:     pe.GetResponse(),
		ResponseMeta: pe.GetResponseMeta(),
		HitCount:     pe.GetHitCount(),
	}
	if t := pe.GetCreatedAtUnix(); t > 0 {
		out.CreatedAt = time.Unix(t, 0).UTC()
	}
	if t := pe.GetExpiresAtUnix(); t > 0 {
		out.ExpiresAt = time.Unix(t, 0).UTC()
	}
	for _, s := range pe.GetSources() {
		out.Sources = append(out.Sources, sourceRef{
			SourceID:    s.GetSourceId(),
			ContentHash: s.GetContentHash(),
		})
	}
	return out
}

func stringSourcesToProto(srcs []sourceRef) ([]*pb.SourceRef, error) {
	if len(srcs) == 0 {
		return nil, nil
	}
	out := make([]*pb.SourceRef, 0, len(srcs))
	for _, s := range srcs {
		// Validate hex content_hash here so the error surfaces on the
		// CLI side rather than a less-helpful gRPC InvalidArgument.
		if s.ContentHash != "" {
			if _, err := hex.DecodeString(s.ContentHash); err != nil {
				return nil, fmt.Errorf("invalid content_hash %q: %w", s.ContentHash, err)
			}
		}
		out = append(out, &pb.SourceRef{
			SourceId:    s.SourceID,
			ContentHash: s.ContentHash,
		})
	}
	return out, nil
}
