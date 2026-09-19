package vm

import (
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestVmShapeOverride_ChainInheritance gates the per-deploy VM-shape override's core
// contract: a deploy that declares nothing inherits the nearest non-empty ram/cpu up its
// from: chain, so ONE declaration at a provisioning root serves every derived clone bed
// (R3). Deleting the chain walk (reading only the created node) fails the clone case below.
func TestVmShapeOverride_ChainInheritance(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			// The lean provisioning root states the shape ONCE.
			"lean-root": {From: "ns.omarchy-vm", Ram: "4G", Cpus: 2},
			// A golden base derives from the lean root, declares no shape.
			"golden-base": {From: "lean-root"},
			// A clone bed derives from the golden base's snapshot, declares no shape.
			"clone-bed": {From: "golden-base", FromSnapshot: "golden"},
		},
	}
	cases := []struct {
		name     string
		identity string
		entity   string
		wantRam  string
		wantCpus int
	}{
		{"the root itself", "lean-root", "lean-root", "4G", 2},
		{"a derived golden base inherits", "golden-base", "golden-base", "4G", 2},
		{"a clone bed inherits through two hops", "clone-bed", "golden-base", "4G", 2},
		{"an unknown deploy declares nothing", "nope", "nope", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ram, cpus := vmShapeOverride(uf, c.entity, c.identity)
			if ram != c.wantRam || cpus != c.wantCpus {
				t.Fatalf("vmShapeOverride(%q,%q) = (%q,%d), want (%q,%d)", c.entity, c.identity, ram, cpus, c.wantRam, c.wantCpus)
			}
		})
	}
}

// TestVmShapeOverride_NearestWins pins the precedence: a deploy that DOES declare a shape
// uses its own, never the inherited one — an override is an override, not an addition.
func TestVmShapeOverride_NearestWins(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"root":   {From: "ns.omarchy-vm", Ram: "8G", Cpus: 4},
			"child":  {From: "root", Ram: "2G", Cpus: 1},
			"nested": {From: "child"},
		},
	}
	if ram, cpus := vmShapeOverride(uf, "root", "child"); ram != "2G" || cpus != 1 {
		t.Fatalf("child should win with its own (2G,1), got (%q,%d)", ram, cpus)
	}
	if ram, cpus := vmShapeOverride(uf, "root", "nested"); ram != "2G" || cpus != 1 {
		t.Fatalf("nested should inherit child's (2G,1), got (%q,%d)", ram, cpus)
	}
}

// TestVmShapeOverride_PartialShape pins that a chain stating ONLY ram keeps the template's
// cpu (the fold is per-field), and vice versa. Deleting the per-field guard folds a zero
// cpus over the template's real one.
func TestVmShapeOverride_PartialShape(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{"ram-only": {From: "ns.omarchy-vm", Ram: "12G"}},
	}
	got := &spec.ResolvedVm{Ram: "8G", Cpus: 4}
	ram, cpus := vmShapeOverride(uf, "ram-only", "ram-only")
	applyVmShapeOverride(got, ram, cpus)
	if got.Ram != "12G" || got.Cpus != 4 {
		t.Fatalf("partial override = (%q,%d), want ram 12G kept cpu 4", got.Ram, got.Cpus)
	}
}

// TestVmShapeOverride_ByIdentity ensures the --domain identity selects the deploy being
// created even when it is a clone bed whose from: is a golden base (not the template). A
// resolver keyed only on the entity positional would read the base's node and miss a
// clone-specific shape.
func TestVmShapeOverride_ByIdentity(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"golden-base": {From: "ns.omarchy-vm"},
			"clone-bed":   {From: "golden-base", Ram: "16G", Cpus: 8},
		},
	}
	// identity = the clone bed, entity = the base it builds from.
	if ram, cpus := vmShapeOverride(uf, "golden-base", "clone-bed"); ram != "16G" || cpus != 8 {
		t.Fatalf("identity-scoped override = (%q,%d), want (16G,8)", ram, cpus)
	}
}
