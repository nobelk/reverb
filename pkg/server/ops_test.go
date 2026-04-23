package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nobelk/reverb/pkg/embedding/fake"
	"github.com/nobelk/reverb/pkg/metrics"
	"github.com/nobelk/reverb/pkg/reverb"
	"github.com/nobelk/reverb/pkg/server"
	"github.com/nobelk/reverb/pkg/store/memory"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func TestHTTP_Readyz_OKWithNoChecks(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "ready", resp["status"])
}

func TestHTTP_Readyz_WithPassingCheck(t *testing.T) {
	client, _ := testClient(t)
	srv := server.NewHTTPServer(client, ":0", nil,
		server.WithReadinessCheck(func(context.Context) error { return nil }),
	)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHTTP_Readyz_FailingCheckReturns503(t *testing.T) {
	client, _ := testClient(t)
	srv := server.NewHTTPServer(client, ":0", nil,
		server.WithReadinessCheck(func(context.Context) error {
			return errors.New("store offline")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "not_ready", resp["status"])
	checks, ok := resp["checks"].(map[string]any)
	require.True(t, ok)
	// First failing check shows up keyed by index.
	assert.Contains(t, checks["check_0"], "store offline")
}

func TestHTTP_Readyz_MultipleChecksAllMustPass(t *testing.T) {
	client, _ := testClient(t)
	srv := server.NewHTTPServer(client, ":0", nil,
		server.WithReadinessCheck(func(context.Context) error { return nil }),
		server.WithReadinessCheck(func(context.Context) error { return errors.New("embedder down") }),
	)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHTTP_Metrics_ExposedOnMux(t *testing.T) {
	reg := prometheus.NewRegistry()
	pc, err := metrics.NewPrometheusCollector(reg)
	require.NoError(t, err)
	// Increment something so the metric family actually appears.
	pc.LookupsTotal.WithLabelValues("ns", "miss").Inc()

	client, _ := testClient(t)
	srv := server.NewHTTPServer(client, ":0", nil, server.WithMetricsOnMux(reg))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "reverb_lookups_total")
}

func TestHTTP_Metrics_NotMountedByDefault(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestMetricsServer_ServesPromHandler(t *testing.T) {
	reg := prometheus.NewRegistry()
	pc, err := metrics.NewPrometheusCollector(reg)
	require.NoError(t, err)
	pc.StoresTotal.WithLabelValues("ns").Inc()

	// Pick an available port via a short-lived listener so the bind is
	// deterministic without racing the test process.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := probe.Addr().String()
	require.NoError(t, probe.Close())

	srv := server.NewMetricsServer(addr, reg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startDone := make(chan error, 1)
	go func() { startDone <- srv.Start(ctx) }()

	// Wait briefly for the listener to come up, then scrape.
	var body string
	var got int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
			got = resp.StatusCode
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	assert.Equal(t, http.StatusOK, got)
	assert.Contains(t, body, "reverb_stores_total")

	cancel()
	select {
	case err := <-startDone:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("MetricsServer.Start did not return after context cancel")
	}
}

// --- graceful shutdown under in-flight load (task 3) -----------------------

// blockingEmbedder blocks each Embed call until unblock is closed, so tests
// can ensure a request is in the handler when shutdown is triggered.
type blockingEmbedder struct {
	unblock  <-chan struct{}
	inflight chan struct{} // non-nil: each Embed sends on this at entry
	dims     int
}

func (b *blockingEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	if b.inflight != nil {
		b.inflight <- struct{}{}
	}
	select {
	case <-b.unblock:
		return make([]float32, b.dims), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		v, err := b.Embed(ctx, texts[i])
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

func (b *blockingEmbedder) Dimensions() int { return b.dims }

func TestHTTPServer_GracefulShutdown_InFlightRequestsComplete(t *testing.T) {
	const dims = 16

	unblock := make(chan struct{})
	inflight := make(chan struct{}, 4)
	embedder := &blockingEmbedder{unblock: unblock, inflight: inflight, dims: dims}

	cfg := reverb.Config{
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	client, err := reverb.New(cfg, embedder, memory.New(), flat.New(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// Probe for an available port, then bind HTTPServer to it via Start.
	// The short window between Close and Start is a deliberate test
	// simplification — in practice Start would bind immediately.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := probe.Addr().String()
	require.NoError(t, probe.Close())

	httpSrv := server.NewHTTPServer(client, addr, nil)

	serveCtx, serveCancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpSrv.Start(serveCtx) }()

	// Wait for the listener to actually bind.
	waitFor(t, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 2*time.Second)

	// Fire a request that will block inside the embedder.
	type result struct {
		status int
		err    error
	}
	done := make(chan result, 1)
	go func() {
		body := strings.NewReader(`{"namespace":"ns","prompt":"p","model_id":"m","response":"r"}`)
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/store", body)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		done <- result{status: resp.StatusCode}
	}()

	// Wait until the handler is actually inside the embedder.
	select {
	case <-inflight:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached embedder")
	}

	// Trigger graceful shutdown by cancelling the serve context. Start will
	// call http.Server.Shutdown which must drain in-flight handlers.
	serveCancel()

	// Serve should not have returned yet — the blocked handler holds it.
	select {
	case err := <-serveDone:
		t.Fatalf("Start returned before in-flight request finished: err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Unblock the embedder. The original request must complete with 201.
	close(unblock)
	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, http.StatusCreated, r.status)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request never completed")
	}

	// Start must now return cleanly.
	select {
	case err := <-serveDone:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after in-flight request completed")
	}

	// New connections to the address should fail: listener has been closed.
	_, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	assert.Error(t, dialErr, "listener should be closed after shutdown")
}

// waitFor polls fn until it returns true or deadline elapses.
func waitFor(t *testing.T, fn func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("waitFor timed out")
}

func TestHTTPServer_Start_ReturnsOnContextCancel(t *testing.T) {
	client, _ := testClient(t)
	httpSrv := server.NewHTTPServer(client, "127.0.0.1:0", nil)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- httpSrv.Start(ctx)
	}()

	// Give the listener a moment to bind.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err, "Start should return nil after graceful shutdown")
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}

	wg.Wait()
}

// --- helpers ---------------------------------------------------------------

func testClient(t *testing.T) (*reverb.Client, *metrics.Collector) {
	t.Helper()
	cfg := reverb.Config{
		DefaultTTL:          time.Hour,
		SimilarityThreshold: 0.95,
		SemanticTopK:        5,
	}
	c, err := reverb.New(cfg, fake.New(64), memory.New(), flat.New(0))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, metrics.NewCollector()
}

