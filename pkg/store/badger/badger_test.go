package badger

import (
	"testing"

	"github.com/org/reverb/pkg/store"
	"github.com/org/reverb/pkg/store/conformance"
)

func TestBadgerConformance(t *testing.T) {
	conformance.RunStoreConformance(t, func(t *testing.T) store.Store {
		s, err := NewInMemory()
		if err != nil {
			t.Fatalf("failed to create in-memory badger store: %v", err)
		}
		return s
	})
}
