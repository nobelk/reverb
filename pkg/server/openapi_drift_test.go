package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestOpenAPIDrift round-trips a sample request through every operation in
// openapi/v1.yaml and asserts the live HTTP handler returns a body whose JSON
// keys match the spec's response schema. The check fires whenever either side
// of the contract changes:
//
//   - If `pkg/server/http.go` adds, removes, or renames a JSON tag, the
//     response will contain a key that is not in the spec (or be missing a
//     required key) and this test fails.
//   - If `openapi/v1.yaml` declares a property that the handler does not
//     emit, the required-field check fails.
//
// The test deliberately uses no OpenAPI tooling so the main repo's dependency
// surface stays stable (per `tech-stack.md` §"Dependency policy").
func TestOpenAPIDrift(t *testing.T) {
	spec := loadSpec(t)
	srv, _ := setupTestServer(t)

	// Seed an entry so /v1/lookup and DELETE /v1/entries/{id} have something
	// to operate on. The store call also exercises StoreResponse.
	entryID := storeEntry(t, srv)

	cases := []driftCase{
		{
			method:     http.MethodPost,
			path:       "/v1/lookup",
			body:       map[string]any{"namespace": "test-ns", "prompt": "What is Go?", "model_id": "gpt-4"},
			wantStatus: http.StatusOK,
		},
		{
			method:     http.MethodPost,
			path:       "/v1/store",
			body:       map[string]any{"namespace": "test-ns", "prompt": "drift probe", "model_id": "gpt-4", "response": "ok"},
			wantStatus: http.StatusCreated,
		},
		{
			method:     http.MethodPost,
			path:       "/v1/invalidate",
			body:       map[string]any{"source_id": "doc:none"},
			wantStatus: http.StatusOK,
		},
		{
			method:     http.MethodGet,
			path:       "/v1/stats",
			wantStatus: http.StatusOK,
		},
		{
			method:     http.MethodGet,
			path:       "/healthz",
			wantStatus: http.StatusOK,
		},
		{
			method:     http.MethodGet,
			path:       "/readyz",
			wantStatus: http.StatusOK,
		},
		{
			method:     http.MethodDelete,
			path:       "/v1/entries/" + entryID,
			specPath:   "/v1/entries/{id}",
			wantStatus: http.StatusNoContent,
		},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			rec := fire(t, srv, c)
			require.Equalf(t, c.wantStatus, rec.Code, "unexpected status; body=%s", rec.Body.String())

			schema := spec.responseSchema(t, c.specPathOrDefault(), strings.ToLower(c.method), c.wantStatus)
			if schema == nil {
				// 204 No Content has no body schema — verify the body is empty.
				require.Empty(t, rec.Body.Bytes(), "expected empty body for %d", c.wantStatus)
				return
			}

			var got any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got), "response is not valid JSON: %s", rec.Body.String())
			diffs := spec.validate(got, schema, "")
			if len(diffs) > 0 {
				sort.Strings(diffs)
				t.Fatalf("openapi/v1.yaml drift for %s %s:\n  - %s",
					c.method, c.path, strings.Join(diffs, "\n  - "))
			}
		})
	}
}

type driftCase struct {
	method     string
	path       string
	specPath   string // optional override when path includes a path-parameter value
	body       any
	wantStatus int
}

func (c driftCase) specPathOrDefault() string {
	if c.specPath != "" {
		return c.specPath
	}
	return c.path
}

func fire(t *testing.T, h http.Handler, c driftCase) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if c.body != nil {
		raw, err := json.Marshal(c.body)
		require.NoError(t, err)
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(c.method, c.path, body)
	if c.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// --- spec loader and validator --------------------------------------------

type openAPISpec struct {
	Paths      map[string]map[string]operation `yaml:"paths"`
	Components struct {
		Schemas   map[string]yaml.Node `yaml:"schemas"`
		Responses map[string]yaml.Node `yaml:"responses"`
	} `yaml:"components"`
}

type operation struct {
	Responses map[string]yaml.Node `yaml:"responses"`
}

func loadSpec(t *testing.T) *openAPISpec {
	t.Helper()
	path := findSpec(t)
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	var s openAPISpec
	require.NoError(t, yaml.Unmarshal(raw, &s), "parse %s", path)
	require.NotEmpty(t, s.Paths, "no paths in spec")
	return &s
}

// findSpec walks up from the test's working directory to locate openapi/v1.yaml.
// Tests run with CWD = package directory (pkg/server), so the file is two
// levels up; walking is robust to refactors.
func findSpec(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for range 8 {
		candidate := filepath.Join(dir, "openapi", "v1.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate openapi/v1.yaml from %s", dir)
	return ""
}

// responseSchema resolves the schema for (path, method, status) following any
// $ref to components/responses and components/schemas. Returns nil when the
// response declares no body (e.g. 204 No Content).
func (s *openAPISpec) responseSchema(t *testing.T, path, method string, status int) map[string]any {
	t.Helper()
	op, ok := s.Paths[path][method]
	require.Truef(t, ok, "spec is missing %s %s", strings.ToUpper(method), path)

	respNode, ok := op.Responses[strconv.Itoa(status)]
	require.Truef(t, ok, "spec is missing %d response for %s %s", status, strings.ToUpper(method), path)

	resolved := s.resolveResponse(t, respNode)
	contentNode, ok := resolved["content"].(map[string]any)
	if !ok {
		return nil
	}
	jsonNode, ok := contentNode["application/json"].(map[string]any)
	if !ok {
		return nil
	}
	schemaNode, ok := jsonNode["schema"].(map[string]any)
	require.True(t, ok, "schema missing for %s %s -> %d", strings.ToUpper(method), path, status)
	return s.resolveSchema(schemaNode)
}

func (s *openAPISpec) resolveResponse(t *testing.T, n yaml.Node) map[string]any {
	t.Helper()
	m, err := nodeToMap(n)
	require.NoError(t, err)
	if ref, ok := m["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/responses/")
		respNode, ok := s.Components.Responses[name]
		require.Truef(t, ok, "unresolved response $ref %q", ref)
		out, err := nodeToMap(respNode)
		require.NoError(t, err)
		return out
	}
	return m
}

func (s *openAPISpec) resolveSchema(m map[string]any) map[string]any {
	if ref, ok := m["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		raw, ok := s.Components.Schemas[name]
		if !ok {
			return m
		}
		out, err := nodeToMap(raw)
		if err != nil {
			return m
		}
		return s.resolveSchema(out)
	}
	return m
}

// validate walks `got` against `schema`. Returns a list of human-readable
// drift messages; empty means clean.
//
// Coverage is intentionally narrow: it catches added/removed/renamed keys at
// every nesting level of objects and arrays-of-objects. It does not enforce
// type equivalence beyond object-vs-non-object — JSON-tag drift is the failure
// mode this guards against, and stronger validation would justify pulling in
// a third-party OpenAPI library.
func (s *openAPISpec) validate(got any, schema map[string]any, path string) []string {
	schema = s.resolveSchema(schema)

	switch typed := got.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		var diffs []string

		// Required keys must be present.
		for _, r := range required {
			name, _ := r.(string)
			if _, ok := typed[name]; !ok {
				diffs = append(diffs, fmt.Sprintf("missing required field %q at %s", name, displayPath(path)))
			}
		}

		// Every key in the response must be declared in the schema, unless
		// the schema permits free-form fields via additionalProperties.
		additional := schema["additionalProperties"]
		for k, v := range typed {
			propSchema, declared := props[k].(map[string]any)
			if !declared {
				if additional == nil || additional == false {
					diffs = append(diffs, fmt.Sprintf("undeclared field %q at %s", k, displayPath(path)))
				}
				continue
			}
			diffs = append(diffs, s.validate(v, propSchema, path+"."+k)...)
		}
		return diffs

	case []any:
		items, _ := schema["items"].(map[string]any)
		if items == nil {
			return nil
		}
		var diffs []string
		for i, item := range typed {
			diffs = append(diffs, s.validate(item, items, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return diffs
	}
	return nil
}

func displayPath(p string) string {
	if p == "" {
		return "(root)"
	}
	return strings.TrimPrefix(p, ".")
}

// nodeToMap converts a yaml.Node to a generic map[string]any. We do this once
// and lazily because yaml.Node retains line/column info we don't need but is
// awkward to traverse directly.
func nodeToMap(n yaml.Node) (map[string]any, error) {
	var out map[string]any
	if err := n.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

