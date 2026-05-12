// Package regex provides a default regex-based Redactor for the four
// most-requested PII categories: email addresses, North American phone
// numbers, credit-card numbers (Luhn-checked), and US Social Security
// numbers.
//
// Replacement form: [EMAIL], [PHONE], [CARD], [SSN]. The redacted prompt
// flows through the normal cache-key path, so two requests that differ only
// in the redacted spans share a cache entry.
//
// Tradeoffs:
//   - This is a pragmatic default, not a complete DLP solution. It will
//     miss obfuscated forms ("five five five..."), non-NA phone patterns,
//     and many international IDs. Operators with stricter requirements
//     should compose their own normalize.Redactor.
//   - The patterns are conservative: false positives are preferred over
//     false negatives. A nine-digit number that matches the SSN shape is
//     redacted even when it is, in fact, an order ID.
package regex

import (
	"context"
	"regexp"
)

// Redactor implements normalize.Redactor with a fixed set of regex rules.
type Redactor struct{}

// New returns a default Redactor. The struct is empty; the function exists
// for symmetry with other constructors and so future configuration knobs
// can be added without breaking call sites.
func New() *Redactor { return &Redactor{} }

// Redact replaces every recognized PII span with its placeholder.
func (*Redactor) Redact(_ context.Context, prompt string) string {
	// Order matters: longest patterns first so a credit-card number is not
	// partially eaten by a generic-digit pattern.
	prompt = emailRe.ReplaceAllString(prompt, "[EMAIL]")
	prompt = creditCardRe.ReplaceAllStringFunc(prompt, func(match string) string {
		if luhn(match) {
			return "[CARD]"
		}
		return match
	})
	prompt = ssnRe.ReplaceAllString(prompt, "[SSN]")
	prompt = phoneRe.ReplaceAllString(prompt, "[PHONE]")
	return prompt
}

// Pre-compiled, package-level — these are read-only and shared across calls.
var (
	// Simplified RFC 5322 — the production-grade form is unwieldy and the
	// stricter forms reject valid addresses. This covers the real-world
	// shapes we care about.
	emailRe = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)

	// North American phone: optional +1, optional separators, three-three-
	// four digits. Anchored to word boundaries. We deliberately accept
	// fictional shapes (555-123-4567 has an invalid NANP exchange but is
	// the most common test number on the planet) — false positives are
	// preferred over false negatives in a redactor.
	phoneRe = regexp.MustCompile(`(?:\+?1[\s.\-]?)?\(?\b[2-9][0-9]{2}\)?[\s.\-]?[0-9]{3}[\s.\-]?[0-9]{4}\b`)

	// Credit-card numbers: 13–19 digits with optional space/dash separators
	// between digits but not after the last digit. The Luhn check downstream
	// rejects coincidental matches.
	creditCardRe = regexp.MustCompile(`\b\d(?:[ \-]?\d){12,18}\b`)

	// US SSN: NNN-NN-NNNN, with separator variants.
	ssnRe = regexp.MustCompile(`\b\d{3}[\s.\-]\d{2}[\s.\-]\d{4}\b`)
)

// luhn returns true iff the digits in s satisfy the Luhn checksum. Non-digit
// characters are ignored.
func luhn(s string) bool {
	var sum int
	parity := 0
	digits := 0
	// Iterate right-to-left so doubled positions are stable regardless of
	// where separators sit.
	for i := len(s) - 1; i >= 0; i-- {
		c := s[i]
		if c < '0' || c > '9' {
			continue
		}
		d := int(c - '0')
		if parity%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		parity++
		digits++
	}
	if digits < 13 || digits > 19 {
		return false
	}
	return sum%10 == 0
}

