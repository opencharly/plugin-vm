package vm

import (
	"testing"
)

// TestApplyBakeCloneSource gates the bake base wiring (Phase 3): the base materializes
// as a clone of the entity's OWN golden at the named snapshot. Removing the
// applyBakeCloneSource wiring from VmBakeCmd.Run fails this test.
func TestApplyBakeCloneSource(t *testing.T) {
	vs := &VmSpec{}
	applyBakeCloneSource(vs, "cachyos-vm", "golden")
	if vs.Source.Kind != "clone" || vs.Source.FromVm != "cachyos-vm" || vs.Source.FromSnapshot != "golden" {
		t.Errorf("bake source = kind=%q from_vm=%q from_snapshot=%q, want clone/cachyos-vm/golden",
			vs.Source.Kind, vs.Source.FromVm, vs.Source.FromSnapshot)
	}
}
