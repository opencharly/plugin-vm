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

// The bake requires a clone source (a layered VM bakes a clone base). This
// test drives the REAL guard (requireCloneSource — the function VmBakeCmd.Run
// calls), so a removed or weakened guard fails the test.
func TestBakeRequiresCloneKind(t *testing.T) {
	nonClone := []string{"cloud_image", "bootc", "bootstrap", "iso", "imported"}
	for _, kind := range nonClone {
		s := &VmSpec{}
		s.Source.Kind = kind
		if err := requireCloneSource(s, "bake-test"); err == nil {
			t.Fatalf("requireCloneSource must refuse source.kind %q, got nil", kind)
		}
	}
	// A clone source passes.
	s := &VmSpec{}
	s.Source.Kind = "clone"
	if err := requireCloneSource(s, "bake-test"); err != nil {
		t.Fatalf("requireCloneSource must accept source.kind clone, got %v", err)
	}
	// A nil spec is refused (the no-entity path).
	if err := requireCloneSource(nil, "bake-test"); err == nil {
		t.Fatal("requireCloneSource must refuse a nil spec")
	}
}
