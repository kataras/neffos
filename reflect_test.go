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

func TestNonZeroFieldsSkipUnexported(t *testing.T) {
	type mixed struct {
		A int
		b int
		C []string
	}
	fields := getNonZeroFields(reflect.ValueOf(&mixed{A: 1, b: 2}))
	if len(fields) != 1 {
		t.Fatalf("expected only the exported non-zero field, got %v", fields)
	}
	if _, ok := fields[0]; !ok {
		t.Fatalf("expected field A (index 0) to be static, got %v", fields)
	}
}

func TestIsZeroKinds(t *testing.T) {
	var nilSlice []int
	var nilMap map[string]int
	var nilFunc func()
	for _, v := range []any{false, 0, "", nilSlice, nilMap, nilFunc, [2]int{}, struct{ A int }{}} {
		if !isZero(reflect.ValueOf(v)) {
			t.Fatalf("expected %T(%v) to be zero", v, v)
		}
	}
	for _, v := range []any{true, 1, "x", []int{}, map[string]int{}, [2]int{0, 1}, struct{ A int }{1}} {
		if isZero(reflect.ValueOf(v)) {
			t.Fatalf("expected %T(%v) to be non-zero", v, v)
		}
	}
}
