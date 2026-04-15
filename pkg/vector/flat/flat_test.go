package flat_test

import (
	"testing"

	"github.com/nobelk/reverb/pkg/vector"
	"github.com/nobelk/reverb/pkg/vector/conformance"
	"github.com/nobelk/reverb/pkg/vector/flat"
)

func TestFlatIndexConformance(t *testing.T) {
	conformance.RunVectorIndexConformance(t, func(t *testing.T, dims int) vector.Index {
		return flat.New(dims)
	})
}
