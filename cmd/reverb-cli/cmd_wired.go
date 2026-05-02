package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// subFlags returns a *flag.FlagSet whose error output and Usage lambda are
// pre-wired to the env's stderr. It also re-registers the global flags
// (--server, --transport, --token, --timeout) on the subcommand's FlagSet
// so `reverb-cli stats --server localhost:8080` works just as well as
// `reverb-cli --server localhost:8080 stats`. Operators expect both
// forms; the validation checklist in this spec explicitly tests the
// after-subcommand form.
//
// We thread the global values through string variables that point back
// into env. The flag package overwrites them in place during Parse, so
// when the subcommand later reads `e.server` etc. it sees the latest
// value regardless of where on the command line it appeared.
func subFlags(e *env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.StringVar(&e.server, "server", e.server, "Reverb server address (overrides global --server)")
	fs.StringVar(&e.transport, "transport", e.transport, "Transport: http or grpc (overrides global --transport)")
	fs.StringVar(&e.token, "token", e.token, "Bearer token (overrides global --token)")
	fs.StringVar(&e.timeout, "timeout", e.timeout, "Per-request timeout (overrides global --timeout)")
	fs.Usage = func() {
		fmt.Fprintln(e.stderr, usage)
		fmt.Fprintln(e.stderr, "\nFlags:")
		fs.PrintDefaults()
	}
	return fs
}

// requireClient resolves a wireClient or prints the error and returns
// (nil, exit-code). Subcommands return that exit code directly.
func requireClient(e *env) (wireClient, int) {
	c, err := e.newClient(e)
	if err != nil {
		fmt.Fprintf(e.stderr, "reverb-cli: %v\n", err)
		return nil, 1
	}
	return c, 0
}

func cmdStats(ctx context.Context, e *env, argv []string) int {
	fs := subFlags(e, "stats", "Usage: reverb-cli [global flags] stats [--json]")
	asJSON := fs.Bool("json", false, "Emit raw JSON instead of human-readable output")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	c, code := requireClient(e)
	if c == nil {
		return code
	}
	defer c.Close()

	resp, err := c.Stats(ctx)
	if err != nil {
		fmt.Fprintf(e.stderr, "stats: %v\n", err)
		return 1
	}

	if *asJSON {
		return writeJSON(e.stdout, resp)
	}
	fmt.Fprintf(e.stdout, "total_entries:        %d\n", resp.TotalEntries)
	fmt.Fprintf(e.stdout, "exact_hits_total:     %d\n", resp.ExactHitsTotal)
	fmt.Fprintf(e.stdout, "semantic_hits_total:  %d\n", resp.SemanticHitsTotal)
	fmt.Fprintf(e.stdout, "misses_total:         %d\n", resp.MissesTotal)
	fmt.Fprintf(e.stdout, "invalidations_total:  %d\n", resp.InvalidationsTotal)
	fmt.Fprintf(e.stdout, "hit_rate:             %.4f\n", resp.HitRate)
	if len(resp.Namespaces) > 0 {
		fmt.Fprintf(e.stdout, "namespaces:           %s\n", strings.Join(resp.Namespaces, ", "))
	}
	return 0
}

func cmdLookup(ctx context.Context, e *env, argv []string) int {
	fs := subFlags(e, "lookup", "Usage: reverb-cli [global flags] lookup --namespace <ns> --prompt <text> [--model <id>]")
	ns := fs.String("namespace", "", "Cache namespace (required)")
	prompt := fs.String("prompt", "", "Prompt text (required; use - to read from stdin)")
	model := fs.String("model", "", "Model ID")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *ns == "" || *prompt == "" {
		fs.Usage()
		return 2
	}
	if *prompt == "-" {
		buf, err := readAllStdin()
		if err != nil {
			fmt.Fprintf(e.stderr, "lookup: read prompt from stdin: %v\n", err)
			return 1
		}
		*prompt = buf
	}

	c, code := requireClient(e)
	if c == nil {
		return code
	}
	defer c.Close()

	resp, err := c.Lookup(ctx, &lookupReq{Namespace: *ns, Prompt: *prompt, ModelID: *model})
	if err != nil {
		fmt.Fprintf(e.stderr, "lookup: %v\n", err)
		return 1
	}
	return writeJSON(e.stdout, resp)
}

func cmdStore(ctx context.Context, e *env, argv []string) int {
	fs := subFlags(e, "store",
		"Usage: reverb-cli [global flags] store --namespace <ns> --prompt <text> --response <text> [--model <id>] [--ttl <duration>] [--source <id>:<hex-hash>]")
	ns := fs.String("namespace", "", "Cache namespace (required)")
	prompt := fs.String("prompt", "", "Prompt text (required)")
	response := fs.String("response", "", "Response text (required; use - to read from stdin)")
	model := fs.String("model", "", "Model ID")
	ttl := fs.String("ttl", "", "TTL as a Go duration (e.g. 24h); 0 / unset means server default")
	var sources stringSliceFlag
	fs.Var(&sources, "source", "Source ref as <source_id>:<hex-content-hash>; repeat per source")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *ns == "" || *prompt == "" || *response == "" {
		fs.Usage()
		return 2
	}
	if *response == "-" {
		buf, err := readAllStdin()
		if err != nil {
			fmt.Fprintf(e.stderr, "store: read response from stdin: %v\n", err)
			return 1
		}
		*response = buf
	}

	srcs, err := parseSources(sources)
	if err != nil {
		fmt.Fprintf(e.stderr, "store: %v\n", err)
		return 2
	}

	var ttlSeconds int
	if *ttl != "" {
		d, err := time.ParseDuration(*ttl)
		if err != nil {
			fmt.Fprintf(e.stderr, "store: invalid --ttl: %v\n", err)
			return 2
		}
		ttlSeconds = int(d.Seconds())
	}

	c, code := requireClient(e)
	if c == nil {
		return code
	}
	defer c.Close()

	resp, err := c.Store(ctx, &storeReq{
		Namespace:  *ns,
		Prompt:     *prompt,
		ModelID:    *model,
		Response:   *response,
		Sources:    srcs,
		TTLSeconds: ttlSeconds,
	})
	if err != nil {
		fmt.Fprintf(e.stderr, "store: %v\n", err)
		return 1
	}
	return writeJSON(e.stdout, resp)
}

func cmdInvalidate(ctx context.Context, e *env, argv []string) int {
	fs := subFlags(e, "invalidate", "Usage: reverb-cli [global flags] invalidate <source-id>")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	sourceID := fs.Arg(0)

	c, code := requireClient(e)
	if c == nil {
		return code
	}
	defer c.Close()

	resp, err := c.Invalidate(ctx, sourceID)
	if err != nil {
		fmt.Fprintf(e.stderr, "invalidate: %v\n", err)
		return 1
	}
	fmt.Fprintf(e.stdout, "invalidated_count: %d\n", resp.InvalidatedCount)
	return 0
}

func cmdWarm(ctx context.Context, e *env, argv []string) int {
	fs := subFlags(e, "warm",
		"Usage: reverb-cli [global flags] warm <jsonl-path>\n\nEach line of <jsonl-path> is a JSON object matching the /v1/store request body.\nUse '-' for stdin.")
	keepGoing := fs.Bool("keep-going", false, "Continue past per-line errors instead of stopping at the first failure")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)

	r, closer, err := openReader(path)
	if err != nil {
		fmt.Fprintf(e.stderr, "warm: %v\n", err)
		return 1
	}
	defer closer()

	c, code := requireClient(e)
	if c == nil {
		return code
	}
	defer c.Close()

	scanner := bufio.NewScanner(r)
	// Bump the buffer well past the default 64KB so individual cache
	// entries (long prompts/responses) don't trip the scanner. Cap is
	// generous; per-request server limit is 1MB anyway.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var lineNo, ok, fail int
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		var req storeReq
		if err := json.Unmarshal(raw, &req); err != nil {
			fail++
			fmt.Fprintf(e.stderr, "warm: line %d: invalid JSON: %v\n", lineNo, err)
			if *keepGoing {
				continue
			}
			return 1
		}
		if _, err := c.Store(ctx, &req); err != nil {
			fail++
			fmt.Fprintf(e.stderr, "warm: line %d: store failed: %v\n", lineNo, err)
			if *keepGoing {
				continue
			}
			return 1
		}
		ok++
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(e.stderr, "warm: scan: %v\n", err)
		return 1
	}
	fmt.Fprintf(e.stdout, "warm: ok=%d fail=%d\n", ok, fail)
	if fail > 0 {
		return 1
	}
	return 0
}

// readAllStdin slurps stdin in full. The body is small (single prompt or
// response), so we don't stream.
func readAllStdin() (string, error) {
	buf, err := io.ReadAll(os.Stdin)
	return string(buf), err
}
