// Package main is a runnable smoke test for the OpenAI-compatible reverse-
// proxy mode. It builds cmd/reverb to a temporary path, starts a fake
// OpenAI upstream, then runs the binary with --proxy openai pointed at the
// fake upstream. It issues two identical requests and asserts the second
// one hits the cache (X-Reverb-Cache: HIT, upstream call count stays at 1).
//
// Run with: `go run ./examples/openai-proxy`.
//
// The example pre-builds the binary (rather than `go run`-ing it inside the
// child process) so the proxy is ready in <1s instead of waiting for go's
// dependency resolution to compile cmd/reverb on every invocation.
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("openai-proxy example failed: %v", err)
	}
}

func run() error {
	// 1. Fake upstream.
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-fake","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"Paris."},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	fmt.Printf("[example] fake upstream listening at %s\n", upstream.URL)

	// 2. Build cmd/reverb to a temp path. Building once up-front (rather
	//    than `go run`-ing inside the child) keeps the wait under a second.
	tmpDir, err := os.MkdirTemp("", "reverb-example-")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	binaryPath := filepath.Join(tmpDir, "reverb")
	build := exec.Command("go", "build", "-o", binaryPath, "github.com/nobelk/reverb/cmd/reverb")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build cmd/reverb: %w", err)
	}

	// 3. Boot the proxy.
	port := freePort()
	cmd := exec.Command(binaryPath,
		"--proxy", "openai",
		"--upstream", upstream.URL,
		"--http-addr", fmt.Sprintf(":%d", port))
	cmd.Stdout = nopWriter{}
	cmd.Stderr = nopWriter{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cmd/reverb: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	proxyURL := fmt.Sprintf("http://localhost:%d", port)
	if err := waitForReady(proxyURL+"/healthz", 10*time.Second); err != nil {
		return fmt.Errorf("proxy never became ready: %w", err)
	}
	fmt.Printf("[example] cmd/reverb proxy listening at %s\n", proxyURL)

	// 3. Two identical requests; second must HIT.
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"What is the capital of France?"}]}`

	r1, b1, err := postJSON(proxyURL+"/v1/chat/completions", body)
	if err != nil {
		return err
	}
	fmt.Printf("[example] round 1: status=%d cache=%s body=%s\n",
		r1.StatusCode, r1.Header.Get("X-Reverb-Cache"), strings.TrimSpace(b1))

	r2, b2, err := postJSON(proxyURL+"/v1/chat/completions", body)
	if err != nil {
		return err
	}
	fmt.Printf("[example] round 2: status=%d cache=%s body=%s\n",
		r2.StatusCode, r2.Header.Get("X-Reverb-Cache"), strings.TrimSpace(b2))

	if r2.Header.Get("X-Reverb-Cache") != "HIT" {
		return fmt.Errorf("round 2 expected HIT, got %q", r2.Header.Get("X-Reverb-Cache"))
	}
	if upstreamCalls.Load() != 1 {
		return fmt.Errorf("upstream calls = %d, expected 1", upstreamCalls.Load())
	}
	if b1 != b2 {
		return fmt.Errorf("cached body diverged from upstream body")
	}

	fmt.Println("[example] OK — second identical request was served from cache")
	return nil
}

func postJSON(u, body string) (*http.Response, string, error) {
	resp, err := http.Post(u, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp, string(raw), err
}

func waitForReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("not ready within %s", timeout)
}

func freePort() int {
	// Bind :0, get the assigned port, close the listener — small race window
	// but acceptable for an example.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("free port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	colon := strings.LastIndex(addr, ":")
	var p int
	_, _ = fmt.Sscanf(addr[colon+1:], "%d", &p)
	return p
}

type nopWriter struct{}

func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
