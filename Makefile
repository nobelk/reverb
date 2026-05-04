.PHONY: build build-cli test test-unit test-integration test-all lint lint-docs bench bench-quality bench-baseline docker docker-cli docker-test clean proto-gen \
        run-server sdk-regen-python sdk-regen-js sdk-smoke-python sdk-smoke-js

# --- Build ---
build:
	go build -o bin/reverb ./cmd/reverb

# --- Build the operator CLI ---
# `reverb-cli` is a separate binary from `reverb` so operators can install
# it on machines that never run a cache. CGO is off — the CLI ships as a
# static binary alongside the SDK release artifacts.
build-cli:
	CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/reverb-cli ./cmd/reverb-cli

# --- Production Docker image ---
docker:
	docker build -t reverb:latest .

# --- reverb-cli Docker image (separate, intentionally tiny base) ---
docker-cli:
	docker build -f Dockerfile.cli -t reverb-cli:latest .

# --- Unit tests (no external deps, runs locally) ---
test-unit:
	go test -race -count=1 -timeout 120s ./pkg/... ./internal/...

# --- Integration tests (starts reverb in Docker, runs tests against it) ---
test-integration: docker
	@echo "Starting reverb server in Docker..."
	@docker rm -f reverb-integration 2>/dev/null || true
	@docker run --rm -d --name reverb-integration -p 8082:8080 reverb:latest
	@echo "Waiting for server to be healthy..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		if curl -sf http://localhost:8082/healthz > /dev/null 2>&1; then break; fi; \
		sleep 1; \
	done
	REVERB_TEST_HTTP_ADDR=http://localhost:8082 \
	go test -count=1 -timeout 60s -tags integration ./test/integration/...; \
	EXIT=$$?; \
	docker stop reverb-integration; \
	exit $$EXIT

# --- All tests inside containers (zero host deps beyond Docker) ---
test-all:
	cd test && docker compose up --build --abort-on-container-exit test-runner

# --- Full containerized test (alias) ---
docker-test: test-all

# --- Convenience alias ---
test: test-unit

# --- Linting ---
lint: lint-docs
	golangci-lint run ./...

# --- Doc lint ---
# Greps the user-facing docs for forbidden strings that signal stale claims
# ("not yet wired", "TODO", "known gap") or unstable-tier language outside
# the MCP block. The MCP surface is the only place "experimental" is allowed
# pre-1.0, per COMPATIBILITY.md §Transport Stability — the per-line filter
# below drops MCP-context lines so the legitimate mentions don't trip the
# guard. Run from `make lint`; CI runs it as a standalone job too.
lint-docs:
	@scripts/lint-docs.sh

# --- Benchmarks ---
bench:
	go test -bench=. -benchmem -benchtime=3s -run='^$$' ./...

# --- Quality benchmarks (correctness evals + latency) ---
bench-quality:
	go test -v -count=1 -timeout 300s -run '^TestEval_' ./benchmark/...
	go test -bench=. -benchmem -benchtime=3s -run='^$$' ./benchmark/...

# --- Published latency baselines (the exact numbers in BENCHMARKS.md) ---
# Stderr is silenced so per-Store INFO logs don't interleave with the
# benchmark output. The eval suite (bench-quality) covers Store + logs.
bench-baseline:
	@go test -bench='BenchmarkLookup_(ExactHit|SemanticHit|Miss)(_ScaledIndex)?$$' \
		-benchmem -benchtime=2s -run='^$$' ./benchmark/... 2>/dev/null \
		| grep -E '^(Benchmark|goos|goarch|pkg|cpu|PASS|ok|FAIL)'

# --- Coverage ---
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./pkg/... ./internal/...
	go tool cover -func=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# --- Proto generation ---
proto-gen:
	protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/server/proto/reverb.proto

# --- Run the standalone server (used by SDK smoke tests) ---
run-server: build
	./bin/reverb

# --- SDK wire-client regeneration ---
# Both targets shell out to openapi-generator-cli via npx so contributors
# don't need a global install. The config files in each sdk/*/openapi-generator/
# directory pin generator + version. The generated client is placed under
# reverb/_wire/ (Python) and src/_wire/ (TS); the hand-rolled facade in
# reverb/__init__.py / src/index.ts is unaffected.
sdk-regen-python:
	@command -v npx >/dev/null 2>&1 || { \
		echo "npx is required for openapi-generator-cli — install Node ≥ 18"; exit 1; }
	cd sdk/python && npx --yes @openapitools/openapi-generator-cli@2.13.4 generate \
		-i ../../openapi/v1.yaml \
		-c openapi-generator/config.yaml \
		-o reverb/_wire

sdk-regen-js:
	@command -v npx >/dev/null 2>&1 || { \
		echo "npx is required for openapi-generator-cli — install Node ≥ 18"; exit 1; }
	cd sdk/js && npx --yes @openapitools/openapi-generator-cli@2.13.4 generate \
		-i ../../openapi/v1.yaml \
		-c openapi-generator/config.yaml \
		-o src/_wire

# --- SDK smoke tests (require a running server on :8080) ---
sdk-smoke-python:
	cd sdk/python && pip install -e . >/dev/null && python scripts/smoke_test.py

sdk-smoke-js:
	cd sdk/js && npm install --no-audit --no-fund >/dev/null \
		&& npm run build >/dev/null \
		&& npm run smoke:node

# --- Cleanup ---
clean:
	cd test && docker compose down -v 2>/dev/null || true
	docker rm -f reverb-integration 2>/dev/null || true
	rm -rf bin/ coverage.out coverage.html data/ \
		sdk/python/.pytest_cache sdk/python/.ruff_cache sdk/python/.mypy_cache \
		sdk/python/build sdk/python/dist sdk/python/*.egg-info \
		sdk/js/dist sdk/js/node_modules
