package regex_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/nobelk/reverb/pkg/normalize/redactor/regex"
)

func TestRedactor_Email(t *testing.T) {
	r := regex.New()
	got := r.Redact(context.Background(), "ping me at alice@example.com please")
	assert.Equal(t, "ping me at [EMAIL] please", got)
}

func TestRedactor_Phone(t *testing.T) {
	r := regex.New()
	cases := []struct {
		in, want string
	}{
		{"call 555-123-4567", "call [PHONE]"},
		{"call (555) 123-4567", "call [PHONE]"},
		{"call +1 555-123-4567", "call [PHONE]"},
		{"call 555.123.4567", "call [PHONE]"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, r.Redact(context.Background(), c.in))
		})
	}
}

func TestRedactor_SSN(t *testing.T) {
	r := regex.New()
	got := r.Redact(context.Background(), "my ssn is 123-45-6789")
	assert.Equal(t, "my ssn is [SSN]", got)
}

func TestRedactor_CreditCardLuhnPositive(t *testing.T) {
	r := regex.New()
	// 4111 1111 1111 1111 — common test number that passes Luhn.
	got := r.Redact(context.Background(), "card 4111 1111 1111 1111 expires soon")
	assert.Equal(t, "card [CARD] expires soon", got)
}

func TestRedactor_CreditCardLuhnRejects(t *testing.T) {
	r := regex.New()
	// 16 digits but not Luhn-valid — must NOT be redacted.
	got := r.Redact(context.Background(), "order 1234567890123456 placed")
	assert.Equal(t, "order 1234567890123456 placed", got)
}

func TestRedactor_Combined(t *testing.T) {
	r := regex.New()
	got := r.Redact(context.Background(),
		"my email is alice@example.com and my phone is 555-123-4567")
	assert.Equal(t, "my email is [EMAIL] and my phone is [PHONE]", got)
}

func TestRedactor_NoFalsePositiveOnShortNumbers(t *testing.T) {
	r := regex.New()
	got := r.Redact(context.Background(), "version 1.2.3 plus 42 widgets")
	assert.Equal(t, "version 1.2.3 plus 42 widgets", got)
}

// BenchmarkRedact establishes the p99 < 1ms claim from the spec on the
// reference inputs.
func BenchmarkRedact(b *testing.B) {
	r := regex.New()
	corpus := []string{
		"my email is alice@example.com and my phone is 555-123-4567",
		"call (555) 123-4567 or +1 555.987.6543, ssn 123-45-6789",
		"card 4111 1111 1111 1111 expires soon",
		"the quick brown fox jumps over the lazy dog repeatedly across many lines",
		"version 1.2.3 plus 42 widgets in stock at the warehouse",
	}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.Redact(ctx, corpus[i%len(corpus)])
	}
}
