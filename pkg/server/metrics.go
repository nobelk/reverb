package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsServer exposes Prometheus metrics (and a dedicated liveness probe)
// on its own listener. Operators typically bind this to an internal-only
// address so metrics scraping is isolated from the public data plane.
type MetricsServer struct {
	server *http.Server
	logger *slog.Logger
}

// NewMetricsServer builds a server that serves promhttp at /metrics against
// the provided gatherer and /healthz for liveness. The returned server is not
// started; call Start to bind the listener.
func NewMetricsServer(addr string, gatherer prometheus.Gatherer) *MetricsServer {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &MetricsServer{
		server: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger: slog.Default(),
	}
}

// Start binds the listener and blocks until ctx is cancelled, then performs a
// graceful shutdown with a 5s deadline.
func (m *MetricsServer) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := m.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.server.Shutdown(shutdownCtx)
	}
}

// Addr returns the configured bind address.
func (m *MetricsServer) Addr() string { return m.server.Addr }
