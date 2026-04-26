package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/nobelk/reverb/pkg/cdc"
)

// Config holds webhook listener configuration.
type Config struct {
	Addr      string // e.g., ":9091"
	Path      string // e.g., "/hooks/source-changed"
	AuthToken string // optional Bearer token
}

// Listener implements cdc.Listener via an HTTP webhook endpoint.
type Listener struct {
	cfg    Config
	server *http.Server
}

// New creates a new webhook Listener with the given configuration.
func New(cfg Config) *Listener {
	return &Listener{cfg: cfg}
}

// payload is the JSON structure accepted by the webhook endpoint.
type payload struct {
	SourceID    string `json:"source_id"`
	ContentHash string `json:"content_hash"`
	Timestamp   string `json:"timestamp"`
}

// Start begins listening for webhook calls. It blocks until the context is
// canceled, at which point the HTTP server is shut down gracefully.
func (l *Listener) Start(ctx context.Context, events chan<- cdc.ChangeEvent) error {
	mux := http.NewServeMux()
	mux.HandleFunc(l.cfg.Path, l.handler(events))

	l.server = &http.Server{
		Addr:              l.cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", l.cfg.Addr)
	if err != nil {
		return fmt.Errorf("webhook listener: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := l.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("webhook shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

// Name returns the listener name.
func (l *Listener) Name() string {
	return "webhook"
}

// handler returns an http.HandlerFunc that processes incoming webhook payloads.
func (l *Listener) handler(events chan<- cdc.ChangeEvent) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Check bearer token if configured (constant-time comparison).
		if l.cfg.AuthToken != "" {
			auth := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(auth, "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(l.cfg.AuthToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var p payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		if p.SourceID == "" {
			http.Error(w, "missing required field: source_id", http.StatusBadRequest)
			return
		}

		event := cdc.ChangeEvent{
			SourceID:       p.SourceID,
			ContentHashHex: p.ContentHash,
		}

		// Decode hex content hash if provided.
		if p.ContentHash != "" {
			decoded, err := hex.DecodeString(p.ContentHash)
			if err != nil {
				http.Error(w, "invalid content_hash: "+err.Error(), http.StatusBadRequest)
				return
			}
			if len(decoded) != 32 {
				http.Error(w, "invalid content_hash: must be exactly 64 hex characters (32 bytes)", http.StatusBadRequest)
				return
			}
			copy(event.ContentHash[:], decoded)
		}

		// Parse timestamp if provided, otherwise use current time.
		if p.Timestamp != "" {
			ts, err := time.Parse(time.RFC3339, p.Timestamp)
			if err != nil {
				http.Error(w, "invalid timestamp: "+err.Error(), http.StatusBadRequest)
				return
			}
			event.Timestamp = ts
		} else {
			event.Timestamp = time.Now().UTC()
		}

		// If the request context is cancelled (e.g. server shutdown) and the
		// channel has no readers ready, return 503 instead of blocking forever.
		select {
		case events <- event:
		case <-r.Context().Done():
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"accepted"}`))
	}
}
