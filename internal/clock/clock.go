// Package clock provides a tiny Clock abstraction shared by packages that
// need to mock time in tests. Production code uses Real(); tests inject a
// FakeClock from internal/testutil.
package clock

import "time"

// Clock returns the current time. The single-method shape lets tests inject
// any type that satisfies it without depending on this package.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Real returns a Clock backed by time.Now.
func Real() Clock { return realClock{} }
