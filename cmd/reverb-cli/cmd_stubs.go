package main

import (
	"context"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/nobelk/reverb/pkg/reverb"
)

// notWiredMessage explains why a subcommand can't run today and points at
// the spec/roadmap entry that tracks the work. Honest stubbing keeps the
// CLI shape stable so callers can be written ahead of the wire surface,
// while avoiding the principle 4 trap of pretending features exist.
//
// Per `specs/2026-04-30-adoption-surface/plan.md` §3, the CLI lists every
// roadmap §1.4 subcommand. The three below need server endpoints that do
// not exist on `/v1/*` yet (Scan-by-namespace, Export, Import). The
// drift-checked `openapi/v1.yaml` is the authoritative HTTP surface, so
// these graduate from stubs once the corresponding endpoints land.
const notWiredMessage = `subcommand %q requires a server endpoint that is not yet wired.

Tracked in:
  specs/2026-04-30-adoption-surface/plan.md §3 (Operator UX)
  specs/roadmap.md §1.4 (reverb-cli operator binary)

Until the endpoint ships, use these alternatives where applicable:
  - evict: invalidate-by-source via 'reverb-cli invalidate <source>'
  - export/import: bulk store via 'reverb-cli warm <jsonl>' (write side only)

`

func notWired(name string, e *env) int {
	fmt.Fprintf(e.stderr, notWiredMessage, name)
	return 64 // EX_USAGE — this is a misuse / feature-gap, not a runtime fault.
}

func cmdEvict(_ context.Context, e *env, _ []string) int {
	return notWired("evict", e)
}

func cmdExport(_ context.Context, e *env, _ []string) int {
	return notWired("export", e)
}

func cmdImport(_ context.Context, e *env, _ []string) int {
	return notWired("import", e)
}

// cmdValidateConfig parses the YAML config and exercises ApplyEnvOverrides +
// ApplyDefaults + Validate in the same order cmd/reverb does at startup. It
// does not open network connections or instantiate backends — operators run
// it in CI before deploying a config change, so it must be a pure local
// check. Skipping ApplyEnvOverrides here previously let the CLI accept
// configs the deployed server rejected (e.g. REVERB_AUTH_API_KEY without a
// listen address); routing through the same Config method keeps the two
// binaries' validation surface in lockstep.
func cmdValidateConfig(_ context.Context, e *env, argv []string) int {
	fs := subFlags(e, "validate-config",
		"Usage: reverb-cli validate-config --config <path>")
	path := fs.String("config", "", "Path to YAML config file (required; use - for stdin)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *path == "" {
		fs.Usage()
		return 2
	}

	r, closer, err := openReader(*path)
	if err != nil {
		fmt.Fprintf(e.stderr, "validate-config: open %s: %v\n", *path, err)
		return 1
	}
	defer closer()

	data, err := io.ReadAll(r)
	if err != nil {
		fmt.Fprintf(e.stderr, "validate-config: read %s: %v\n", *path, err)
		return 1
	}

	cfg := reverb.DefaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(e.stderr, "validate-config: parse YAML: %v\n", err)
		return 1
	}
	cfg.ApplyEnvOverrides()
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(e.stderr, "validate-config: invalid: %v\n", err)
		return 1
	}
	fmt.Fprintln(e.stdout, "validate-config: ok")
	return 0
}
