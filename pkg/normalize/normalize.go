package normalize

import (
	"context"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Redactor strips personally-identifiable information from a prompt before
// it is hashed and stored. Implementations must be safe for concurrent use
// and should be deterministic — the same input must always produce the same
// output, since the result feeds the cache key.
//
// The Redact contract: input is a normalized prompt (post-Normalize); output
// is the redacted form, which both feeds the SHA-256 cache key and is
// persisted as the entry's PromptText. This means enabling redaction on an
// existing cache invalidates prior entries by construction (the hash
// changes); operators should expect a one-time hit-rate drop.
type Redactor interface {
	Redact(ctx context.Context, prompt string) string
}

// RedactorFunc adapts a plain function to the Redactor interface.
type RedactorFunc func(ctx context.Context, prompt string) string

// Redact implements Redactor.
func (f RedactorFunc) Redact(ctx context.Context, prompt string) string { return f(ctx, prompt) }

var whitespaceRe = regexp.MustCompile(`\s+`)

// Normalize applies a series of deterministic transformations to reduce
// surface variation between semantically identical prompts.
func Normalize(s string) string {
	// 1. Unicode NFC normalization
	s = norm.NFC.String(s)

	// 2. Lowercase
	s = strings.ToLower(s)

	// 3. Collapse internal whitespace
	s = whitespaceRe.ReplaceAllString(s, " ")

	// 4. Trim
	s = strings.TrimSpace(s)

	// 5. Strip trailing sentence-ending punctuation and any surrounding spaces,
	//    repeating until stable (handles cases like "hello !" or "end . ! ;").
	for {
		t := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), ".?!;"))
		if t == s {
			break
		}
		s = t
	}

	return s
}
