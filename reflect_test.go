package neffos

import (
	"reflect"
	"testing"
)

// zeroNoReturn has an IsZero method with the wrong shape (no result), which
// isZero must ignore instead of calling.
type zeroNoReturn struct{ N int }

func (zeroNoReturn) IsZero() {}

// zeroCustom reports zero through its own IsZero method.
type zeroCustom struct{ N int }

func (z zeroCustom) IsZero() bool { return z.N == 42 }

func TestIsZeroMethodWithoutReturnDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("isZero panicked: %v", r)
		}
	}()

	if !isZero(reflect.ValueOf(zeroNoReturn{})) {
		t.Fatal("expected a zero struct with a malformed IsZero to be zero by its fields")
	}
	if isZero(reflect.ValueOf(zeroNoReturn{N: 1})) {
		t.Fatal("expected a non-zero struct with a malformed IsZero to be non-zero by its fields")
	}
	if !isZero(reflect.ValueOf(zeroCustom{N: 42})) {
		t.Fatal("expected a well-formed IsZero to be used")
	}
}
