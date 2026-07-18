package objectstore

import (
	"errors"
	"testing"
)

func TestObjectStoreMinIOImplementsStore(t *testing.T) {
	var _ Store = (*MinIOStore)(nil)
}

func TestObjectStoreSentinelErrorsRemainComparable(t *testing.T) {
	for _, err := range []error{ErrNotFound, ErrConflict, ErrUnavailable} {
		if !errors.Is(err, err) {
			t.Fatalf("sentinel error is not comparable: %v", err)
		}
	}
}
