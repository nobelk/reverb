// Command reverb-cli is the operator CLI for a running reverb server.
//
// Subcommands cover the workflows that previously required raw curl or
// custom Go code: cache stats, ad-hoc lookup/store, source-driven
// invalidation, bulk warming from JSONL, and config validation. Talks the
// versioned /v1/ HTTP surface by default; pass --transport grpc to reach
// the gRPC service exposed by pkg/server/proto instead.
//
// reverb-cli is a separate binary from cmd/reverb on purpose: it does not
// depend on the server-side stack (stores, vector indices, embedders), so
// operators can install it on machines that never run a cache.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// version is the CLI version string. Overridden at build time via
// -ldflags "-X main.version=<tag>".
var version = "dev"

// commandFunc runs a subcommand. argv excludes the subcommand name itself.
type commandFunc func(ctx context.Context, env *env, argv []string) int

// env carries values resolved from global flags / environment that every
// subcommand may need: the wire client builder, the output streams, and
// the global flag values. Subcommands receive it instead of pulling from
// package globals so tests can inject substitutes.
type env struct {
	stdout io.Writer
	stderr io.Writer

	server    string
	transport string
	token     string
	timeout   string

	// newClient builds a wire client. Tests override it to point at an
	// httptest.Server or in-process gRPC server.
	newClient func(*env) (wireClient, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reverb-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUsage(stderr) }

	server := fs.String("server", envOrDefault("REVERB_SERVER", "http://localhost:8080"), "Reverb server address (HTTP URL or host:port for gRPC)")
	transport := fs.String("transport", envOrDefault("REVERB_TRANSPORT", "http"), "Transport: http or grpc")
	token := fs.String("token", os.Getenv("REVERB_TOKEN"), "Bearer token for authenticated servers")
	timeout := fs.String("timeout", envOrDefault("REVERB_TIMEOUT", "30s"), "Per-request timeout (Go duration string)")
	showVersion := fs.Bool("version", false, "Print version and exit")

	if err := fs.Parse(args); err != nil {
		// flag already printed the error to stderr.
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		writeUsage(stderr)
		return 2
	}

	e := &env{
		stdout:    stdout,
		stderr:    stderr,
		server:    *server,
		transport: *transport,
		token:     *token,
		timeout:   *timeout,
		newClient: defaultNewClient,
	}

	name, argv := rest[0], rest[1:]
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "reverb-cli: unknown command %q\n\n", name)
		writeUsage(stderr)
		return 2
	}
	return cmd(ctx, e, argv)
}

// commands is the subcommand dispatch table. Listed here in usage order so
// `reverb-cli` (no args) prints them in the same sequence the docs use.
var commands = map[string]commandFunc{
	"stats":           cmdStats,
	"lookup":          cmdLookup,
	"store":           cmdStore,
	"invalidate":      cmdInvalidate,
	"evict":           cmdEvict,
	"warm":            cmdWarm,
	"export":          cmdExport,
	"import":          cmdImport,
	"validate-config": cmdValidateConfig,
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: reverb-cli [global flags] <command> [command flags] [args]

Global flags:
  --server     Reverb server address (default http://localhost:8080; env REVERB_SERVER)
  --transport  Transport: http (default) or grpc (env REVERB_TRANSPORT)
  --token      Bearer token for authenticated servers (env REVERB_TOKEN)
  --timeout    Per-request timeout (default 30s; env REVERB_TIMEOUT)
  --version    Print version and exit

Commands:
  stats              Print cache statistics.
  lookup             Look up a cached response for a prompt.
  store              Write a new cache entry.
  invalidate         Invalidate all entries for a source.
  evict              Evict entries by namespace.            (requires server endpoint not yet wired)
  warm               Bulk-load cache entries from a JSONL file.
  export             Export entries to JSONL.               (requires server endpoint not yet wired)
  import             Import entries from a JSONL file.      (requires server endpoint not yet wired)
  validate-config    Parse and validate a server YAML config.

Run 'reverb-cli <command> --help' for command-specific flags.`)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
