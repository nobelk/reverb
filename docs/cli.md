# `reverb-cli` — operator CLI

`reverb-cli` is a separate binary from the `reverb` server. It speaks the
versioned `/v1/*` HTTP surface (and the matching gRPC service in
`pkg/server/proto`) so operators can perform the routine workflows that
otherwise require `curl` or custom Go code:

- inspect cache stats
- ad-hoc lookup / store
- source-driven invalidation
- bulk-warm a cache from a JSONL file
- validate a server YAML config locally before deploying

## Install

```sh
# From source (Go 1.25+)
make build-cli
./bin/reverb-cli --version
```

Pre-built binaries and a Docker image are published on tagged releases via
`.github/workflows/release-cli.yml`:

```sh
# Binary, picks up GOOS/GOARCH naming convention used by the release job.
curl -L https://github.com/nobelk/reverb/releases/download/v0.1.0/reverb-cli_v0.1.0_linux_amd64 \
  -o /usr/local/bin/reverb-cli && chmod +x /usr/local/bin/reverb-cli

# Container
docker pull ghcr.io/nobelk/reverb/reverb-cli:latest
```

A Homebrew tap formula will land alongside the first tagged release; this
doc will be updated to reference the tap once the tag is cut.

## Global flags

| Flag          | Env                | Default                  | Description |
|---------------|--------------------|--------------------------|-------------|
| `--server`    | `REVERB_SERVER`    | `http://localhost:8080`  | Server address. For HTTP, a full URL; for gRPC, `host:port`. |
| `--transport` | `REVERB_TRANSPORT` | `http`                   | `http` or `grpc`. |
| `--token`     | `REVERB_TOKEN`     | (unset)                  | Bearer token for authenticated servers. Sent as `Authorization: Bearer <token>` (HTTP) or the `authorization` metadata key (gRPC). |
| `--timeout`   | `REVERB_TIMEOUT`   | `30s`                    | Per-request timeout (Go duration). |
| `--version`   | —                  | —                        | Print version and exit. |

## Subcommands

### `stats`

Print cache statistics. Useful as a quick smoke test against a running
server.

```sh
reverb-cli stats
# total_entries:        42
# exact_hits_total:     10
# semantic_hits_total:  5
# misses_total:         5
# invalidations_total:  0
# hit_rate:             0.7500
# namespaces:           default, knowledge-base

# Or as JSON for piping into jq / dashboards
reverb-cli stats --json | jq .hit_rate
```

### `lookup`

Look up a cached response.

```sh
reverb-cli lookup --namespace default --prompt "What is Reverb?"

# Read the prompt from stdin
echo "What is Reverb?" | reverb-cli lookup --namespace default --prompt -
```

A miss returns `{"hit": false}`; a hit includes the cached entry, the
matched tier (`exact` or `semantic`), and the similarity score.

### `store`

Write a new cache entry.

```sh
reverb-cli store \
  --namespace default \
  --prompt "What is Reverb?" \
  --response "Reverb is a semantic response cache." \
  --model gpt-4 \
  --ttl 24h \
  --source kb-faq:0123abcd
```

`--source <id>:<hex-content-hash>` may be repeated. A response longer
than the shell's command line tolerates can be piped:

```sh
cat answer.txt | reverb-cli store \
  --namespace default --prompt "..." --response -
```

### `invalidate <source-id>`

Invalidate every cache entry that lists the given source in its lineage.
This is the standard "this knowledge-base doc changed, drop derived
answers" workflow.

```sh
reverb-cli invalidate kb-faq
# invalidated_count: 3
```

### `warm <jsonl-path>`

Bulk-load entries from a JSONL file. Each non-empty, non-`#` line is a
JSON object whose shape matches the `POST /v1/store` request body.

```sh
cat warm.jsonl
# {"namespace":"default","prompt":"hello","response":"world"}
# # comments and blank lines are skipped
# {"namespace":"default","prompt":"how are you","response":"fine"}

reverb-cli warm warm.jsonl
# warm: ok=2 fail=0

# Stream from stdin
cat warm.jsonl | reverb-cli warm -

# Don't stop at the first bad line — accumulate and report
reverb-cli warm --keep-going warm.jsonl
```

### `validate-config`

Parse and validate a server YAML config locally. Does not contact a
server, so it is safe to run in CI before applying a config change.

```sh
reverb-cli validate-config --config deploy/reverb.yaml
# validate-config: ok
```

Mirrors the same `Validate` + `ApplyDefaults` path that `cmd/reverb` runs
at startup.

### `evict --namespace`, `export`, `import` — **not yet wired**

These three subcommands appear in the CLI surface for forward
compatibility. Today they exit with code `64` (`EX_USAGE`) and a
pointer to the spec entry tracking the work, because they require server
endpoints that the `/v1/*` HTTP surface does not yet expose.

Workarounds where applicable:

- `evict` — for now, identify the source(s) feeding a namespace and use
  `reverb-cli invalidate <source>`.
- `export` / `import` — the write side is covered by `warm`; bulk export
  has no current alternative.

The endpoints will graduate from "not yet wired" once they appear in
`openapi/v1.yaml` (the `pkg/server/openapi_drift_test.go` drift check is
the gate).

## Transports

The CLI defaults to HTTP. Switch to gRPC with `--transport grpc`:

```sh
reverb-cli --transport grpc --server localhost:9090 stats
```

The two transports speak the same wire types (`pkg/server/proto`); the
output of equivalent commands is identical. gRPC support uses the
`insecure` transport credential — operators terminate TLS at a load
balancer or service-mesh sidecar.

## Exit codes

| Code | Meaning |
|------|---------|
| `0`  | Success. |
| `1`  | Runtime error (server unreachable, decode failure, store error, ...). |
| `2`  | Usage error (missing flag, bad value, unknown command). |
| `64` | Subcommand requires a server endpoint that is not yet wired. |
