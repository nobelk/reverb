package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// stringSliceFlag accumulates repeated --flag values into a slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseSources turns "<source-id>:<hex-hash>" strings into wire sourceRefs.
// The empty list is valid (no sources).
func parseSources(raw []string) ([]sourceRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]sourceRef, 0, len(raw))
	for _, s := range raw {
		idx := strings.LastIndex(s, ":")
		if idx <= 0 || idx == len(s)-1 {
			return nil, fmt.Errorf("invalid --source %q (want <source_id>:<hex-hash>)", s)
		}
		id, hexHash := s[:idx], s[idx+1:]
		if _, err := hex.DecodeString(hexHash); err != nil {
			return nil, fmt.Errorf("invalid --source %q: hash is not hex: %w", s, err)
		}
		out = append(out, sourceRef{SourceID: id, ContentHash: hexHash})
	}
	return out, nil
}

// openReader returns a reader for path. "-" means stdin. The closer is
// always non-nil and safe to defer; for stdin it's a no-op.
func openReader(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, func() {}, err
	}
	return f, func() { _ = f.Close() }, nil
}

// writeJSON pretty-prints v as JSON to w. Returns the exit code (0 on
// success, 1 on encode failure) so callers can `return writeJSON(...)`.
func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "reverb-cli: encode output: %v\n", err)
		return 1
	}
	return 0
}
