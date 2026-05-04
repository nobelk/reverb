package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClient is a wireClient that records inputs and returns canned outputs.
// Lets command tests stay decoupled from HTTP/gRPC plumbing.
type fakeClient struct {
	statsResp  *statsResp
	statsErr   error
	lookupReqs []*lookupReq
	lookupResp *lookupResp
	storeReqs  []*storeReq
	storeResp  *storeResp
	storeErr   error
	invSources []string
	invResp    *invalidateResp
	invErr     error
	closed     bool
}

func (f *fakeClient) Close() error { f.closed = true; return nil }

func (f *fakeClient) Stats(_ context.Context) (*statsResp, error) {
	return f.statsResp, f.statsErr
}

func (f *fakeClient) Lookup(_ context.Context, req *lookupReq) (*lookupResp, error) {
	f.lookupReqs = append(f.lookupReqs, req)
	return f.lookupResp, nil
}

func (f *fakeClient) Store(_ context.Context, req *storeReq) (*storeResp, error) {
	f.storeReqs = append(f.storeReqs, req)
	return f.storeResp, f.storeErr
}

func (f *fakeClient) Invalidate(_ context.Context, sourceID string) (*invalidateResp, error) {
	f.invSources = append(f.invSources, sourceID)
	return f.invResp, f.invErr
}

func newTestEnv(fc *fakeClient, stdout, stderr *bytes.Buffer) *env {
	return &env{
		stdout:    stdout,
		stderr:    stderr,
		server:    "http://localhost:8080",
		transport: "http",
		timeout:   "30s",
		newClient: func(*env) (wireClient, error) { return fc, nil },
	}
}

func TestRun_NoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr missing usage banner:\n%s", stderr.String())
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"bogus"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code: got %d want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"bogus\"") {
		t.Errorf("stderr missing unknown-command msg:\n%s", stderr.String())
	}
}

func TestRun_VersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--version"}, &stdout, &stderr)
	if code != 0 || strings.TrimSpace(stdout.String()) == "" {
		t.Fatalf("got code=%d stdout=%q", code, stdout.String())
	}
}

func TestCmdStats_Human(t *testing.T) {
	fc := &fakeClient{statsResp: &statsResp{
		TotalEntries:      42,
		ExactHitsTotal:    10,
		SemanticHitsTotal: 5,
		MissesTotal:       5,
		HitRate:           0.75,
		Namespaces:        []string{"a", "b"},
	}}
	var stdout, stderr bytes.Buffer
	code := cmdStats(context.Background(), newTestEnv(fc, &stdout, &stderr), nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"total_entries:        42", "hit_rate:             0.7500", "a, b"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if !fc.closed {
		t.Error("client.Close not called")
	}
}

func TestCmdStats_JSON(t *testing.T) {
	fc := &fakeClient{statsResp: &statsResp{TotalEntries: 7}}
	var stdout, stderr bytes.Buffer
	code := cmdStats(context.Background(), newTestEnv(fc, &stdout, &stderr), []string{"--json"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var got statsResp
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.TotalEntries != 7 {
		t.Errorf("total_entries: got %d want 7", got.TotalEntries)
	}
}

func TestCmdLookup_RequiresFlags(t *testing.T) {
	fc := &fakeClient{}
	var stdout, stderr bytes.Buffer
	code := cmdLookup(context.Background(), newTestEnv(fc, &stdout, &stderr), nil)
	if code != 2 {
		t.Fatalf("code=%d", code)
	}
	if len(fc.lookupReqs) != 0 {
		t.Errorf("client should not be called when args missing")
	}
}

func TestCmdLookup_OK(t *testing.T) {
	fc := &fakeClient{lookupResp: &lookupResp{Hit: true, Tier: "exact"}}
	var stdout, stderr bytes.Buffer
	args := []string{"--namespace", "ns1", "--prompt", "hello"}
	code := cmdLookup(context.Background(), newTestEnv(fc, &stdout, &stderr), args)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(fc.lookupReqs) != 1 || fc.lookupReqs[0].Namespace != "ns1" || fc.lookupReqs[0].Prompt != "hello" {
		t.Errorf("unexpected lookup request: %+v", fc.lookupReqs)
	}
	if !strings.Contains(stdout.String(), `"hit": true`) {
		t.Errorf("stdout missing hit=true:\n%s", stdout.String())
	}
}

func TestCmdStore_OK(t *testing.T) {
	fc := &fakeClient{storeResp: &storeResp{ID: "id-1"}}
	var stdout, stderr bytes.Buffer
	args := []string{
		"--namespace", "ns1", "--prompt", "p", "--response", "r",
		"--source", "src-a:00",
		"--ttl", "1h",
	}
	code := cmdStore(context.Background(), newTestEnv(fc, &stdout, &stderr), args)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(fc.storeReqs) != 1 {
		t.Fatalf("storeReqs=%d", len(fc.storeReqs))
	}
	got := fc.storeReqs[0]
	if got.TTLSeconds != 3600 {
		t.Errorf("ttl_seconds: got %d want 3600", got.TTLSeconds)
	}
	if len(got.Sources) != 1 || got.Sources[0].SourceID != "src-a" || got.Sources[0].ContentHash != "00" {
		t.Errorf("sources: %+v", got.Sources)
	}
}

func TestCmdStore_InvalidSourceFlag(t *testing.T) {
	fc := &fakeClient{}
	var stdout, stderr bytes.Buffer
	args := []string{
		"--namespace", "ns1", "--prompt", "p", "--response", "r",
		"--source", "no-colon-here",
	}
	code := cmdStore(context.Background(), newTestEnv(fc, &stdout, &stderr), args)
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid --source") {
		t.Errorf("stderr missing invalid-source msg:\n%s", stderr.String())
	}
}

func TestCmdInvalidate_OK(t *testing.T) {
	fc := &fakeClient{invResp: &invalidateResp{InvalidatedCount: 3}}
	var stdout, stderr bytes.Buffer
	code := cmdInvalidate(context.Background(), newTestEnv(fc, &stdout, &stderr), []string{"src-x"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(fc.invSources) != 1 || fc.invSources[0] != "src-x" {
		t.Errorf("invSources: %v", fc.invSources)
	}
	if !strings.Contains(stdout.String(), "invalidated_count: 3") {
		t.Errorf("stdout: %s", stdout.String())
	}
}

func TestCmdWarm_JSONLHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warm.jsonl")
	body := `{"namespace":"ns","prompt":"p1","response":"r1"}` + "\n" +
		`# a comment line should be skipped` + "\n" +
		`{"namespace":"ns","prompt":"p2","response":"r2"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{storeResp: &storeResp{ID: "id"}}
	var stdout, stderr bytes.Buffer
	code := cmdWarm(context.Background(), newTestEnv(fc, &stdout, &stderr), []string{path})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if len(fc.storeReqs) != 2 {
		t.Errorf("storeReqs=%d (want 2 — comment line should skip)", len(fc.storeReqs))
	}
	if !strings.Contains(stdout.String(), "ok=2 fail=0") {
		t.Errorf("stdout: %s", stdout.String())
	}
}

func TestCmdWarm_KeepGoingPastBadLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "warm.jsonl")
	body := `{"namespace":"ns","prompt":"p1","response":"r1"}` + "\n" +
		`{not valid json}` + "\n" +
		`{"namespace":"ns","prompt":"p3","response":"r3"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{storeResp: &storeResp{ID: "id"}}
	var stdout, stderr bytes.Buffer
	code := cmdWarm(context.Background(), newTestEnv(fc, &stdout, &stderr), []string{"--keep-going", path})
	if code != 1 {
		t.Fatalf("expected exit 1 (errors present), got %d", code)
	}
	if len(fc.storeReqs) != 2 {
		t.Errorf("storeReqs=%d (want 2 — bad line skipped)", len(fc.storeReqs))
	}
	if !strings.Contains(stdout.String(), "ok=2 fail=1") {
		t.Errorf("stdout: %s", stdout.String())
	}
}

func TestCmdValidateConfig_OK(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("# empty config relies on defaults\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	e := newTestEnv(&fakeClient{}, &stdout, &stderr)
	code := cmdValidateConfig(context.Background(), e, []string{"--config", path})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("stdout: %s", stdout.String())
	}
}

// TestCmdValidateConfig_AppliesEnvOverrides pins parity with cmd/reverb's
// startup: REVERB_AUTH_API_KEY enables auth and appends a tenant, after
// which Validate trips because no listen address is configured. Before this
// regression test, validate-config skipped env overrides and reported "ok"
// for the same input the deployed server rejected.
func TestCmdValidateConfig_AppliesEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("# empty config relies on defaults\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVERB_AUTH_API_KEY", "secret")
	var stdout, stderr bytes.Buffer
	e := newTestEnv(&fakeClient{}, &stdout, &stderr)
	code := cmdValidateConfig(context.Background(), e, []string{"--config", path})
	if code == 0 {
		t.Fatalf("expected non-zero exit when env enables auth without a listen addr; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "auth") {
		t.Errorf("expected auth-related error in stderr, got: %s", stderr.String())
	}
}

func TestCmdValidateConfig_BadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte(": : not yaml ::"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	e := newTestEnv(&fakeClient{}, &stdout, &stderr)
	code := cmdValidateConfig(context.Background(), e, []string{"--config", path})
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStubCommands_ExitWithUsageCode(t *testing.T) {
	for _, name := range []string{"evict", "export", "import"} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := commands[name]
			code := cmd(context.Background(), newTestEnv(&fakeClient{}, &stdout, &stderr), nil)
			if code != 64 {
				t.Errorf("%s: exit %d, want 64", name, code)
			}
			if !strings.Contains(stderr.String(), "not yet wired") {
				t.Errorf("%s: stderr missing 'not yet wired':\n%s", name, stderr.String())
			}
		})
	}
}

// TestHTTPClient_RoundTrip exercises the real HTTP client against an
// httptest.Server so the JSON request/response shapes stay aligned with
// what `pkg/server/http.go` accepts.
func TestHTTPClient_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/stats":
			if r.Method != http.MethodGet {
				t.Errorf("stats method=%s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"total_entries":1,"hit_rate":1}`))
		case "/v1/lookup":
			if got := r.Header.Get("Authorization"); got != "Bearer t" {
				t.Errorf("auth header: %q", got)
			}
			var req lookupReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.Namespace != "ns" || req.Prompt != "p" {
				t.Errorf("lookup body: %+v", req)
			}
			_, _ = w.Write([]byte(`{"hit":true,"tier":"exact"}`))
		case "/v1/store":
			_, _ = w.Write([]byte(`{"id":"id-1","created_at":"2026-05-02T00:00:00Z"}`))
		case "/v1/invalidate":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["source_id"] != "src" {
				t.Errorf("invalidate body source_id=%q", body["source_id"])
			}
			_, _ = w.Write([]byte(`{"invalidated_count":2}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := newHTTPClient(srv.URL, "t", 5_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	if got, err := c.Stats(ctx); err != nil || got.TotalEntries != 1 {
		t.Fatalf("Stats: got %+v err=%v", got, err)
	}
	if got, err := c.Lookup(ctx, &lookupReq{Namespace: "ns", Prompt: "p"}); err != nil || !got.Hit {
		t.Fatalf("Lookup: got %+v err=%v", got, err)
	}
	if got, err := c.Store(ctx, &storeReq{Namespace: "ns", Prompt: "p", Response: "r"}); err != nil || got.ID != "id-1" {
		t.Fatalf("Store: got %+v err=%v", got, err)
	}
	if got, err := c.Invalidate(ctx, "src"); err != nil || got.InvalidatedCount != 2 {
		t.Fatalf("Invalidate: got %+v err=%v", got, err)
	}
}

func TestHTTPClient_ErrorBodyDecoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"namespace is required"}`))
	}))
	defer srv.Close()

	c, _ := newHTTPClient(srv.URL, "", 5_000_000_000)
	defer c.Close()
	_, err := c.Lookup(context.Background(), &lookupReq{})
	if err == nil || !strings.Contains(err.Error(), "namespace is required") {
		t.Errorf("expected error to surface server message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected status code in error, got: %v", err)
	}
}

func TestHTTPClient_ServerHostWithoutScheme(t *testing.T) {
	// Verifies the heuristic in newHTTPClient that prepends http:// when
	// the operator passes 'localhost:8080' instead of a full URL.
	c, err := newHTTPClient("localhost:9999", "", 5_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.base, "http://") {
		t.Errorf("base=%q (want http:// prefix)", c.base)
	}
}

func TestParseSources(t *testing.T) {
	out, err := parseSources([]string{"src:abc123", "src2:00ff"})
	if err != nil || len(out) != 2 || out[0].ContentHash != "abc123" {
		t.Fatalf("ok parse: out=%+v err=%v", out, err)
	}
	if _, err := parseSources([]string{"missing-colon"}); err == nil {
		t.Errorf("expected error for missing colon")
	}
	if _, err := parseSources([]string{"src:zzz"}); err == nil {
		t.Errorf("expected error for non-hex hash")
	}
}

func TestNewClient_UnknownTransport(t *testing.T) {
	_, err := defaultNewClient(&env{server: "x", transport: "ftp", timeout: "5s"})
	if err == nil || !strings.Contains(err.Error(), "unknown --transport") {
		t.Errorf("got %v", err)
	}
}

func TestNewClient_BadTimeout(t *testing.T) {
	_, err := defaultNewClient(&env{server: "x", transport: "http", timeout: "not-a-duration"})
	if err == nil {
		t.Errorf("expected error for bad timeout")
	}
}

// Operators expect global flags to be accepted both before and after the
// subcommand name (`reverb-cli --server X stats` and
// `reverb-cli stats --server X`). The validation checklist in
// `specs/2026-04-30-adoption-surface/validation.md` explicitly tests the
// after-subcommand form, so this test guards against regression.
func TestCmdStats_GlobalFlagAfterSubcommand(t *testing.T) {
	fc := &fakeClient{statsResp: &statsResp{}}
	var stdout, stderr bytes.Buffer
	e := newTestEnv(fc, &stdout, &stderr)
	e.server = "http://default:8080"
	code := cmdStats(context.Background(), e, []string{"--server", "http://override:9090"})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if e.server != "http://override:9090" {
		t.Errorf("e.server after subcommand parse: got %q want override", e.server)
	}
}

// Sanity: when stats RPC fails, exit code propagates and the message
// reaches stderr instead of being swallowed.
func TestCmdStats_ServerErrorSurfaces(t *testing.T) {
	fc := &fakeClient{statsErr: errors.New("upstream unavailable")}
	var stdout, stderr bytes.Buffer
	code := cmdStats(context.Background(), newTestEnv(fc, &stdout, &stderr), nil)
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "upstream unavailable") {
		t.Errorf("stderr: %s", stderr.String())
	}
}
