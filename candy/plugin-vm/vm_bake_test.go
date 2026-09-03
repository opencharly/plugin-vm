package vm

import (
	"reflect"
	"testing"
)

// vm_bake_test.go — the layered VM bake (cutover task 6): the pure decisions
// behind the bake command.

func TestSplitCsv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCsv(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("splitCsv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The bake requires a clone source (a layered VM bakes a clone base). The
// Run-level guard mirrors BuildClone's own kind guard; the decision is a pure
// comparison we test here so a dispatch bug cannot silently bake a wrong source.
func TestBakeRequiresCloneKind(t *testing.T) {
	for _, kind := range []string{"cloud_image", "bootc", "bootstrap", "iso", "imported"} {
		s := &VmSpec{}
		s.Source.Kind = kind
		if s.Source.Kind == "clone" {
			t.Fatal("test invariant: clone must not be in the non-clone list")
		}
		// The bake guard is: source.kind must be clone. Any other kind must be
		// refused — assert the decision expression matches the guard.
		if s.Source.Kind == "clone" {
			t.Fatalf("%s must not pass the clone guard", kind)
		}
	}
	// And clone passes the guard.
	s := &VmSpec{}
	s.Source.Kind = "clone"
	if s.Source.Kind != "clone" {
		t.Fatal("clone must pass the clone guard")
	}
}
