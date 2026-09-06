package vm

import (
	"testing"
)

// TestBakeUsesTheDriveCloneWiring gates the bake base wiring (Phase 3): the base
// materializes as a clone of the entity's OWN golden at the named snapshot, via the SAME
// extracted seam as the build drive (applyCloneDriveSource — R3, one wiring, two
// consumers). Removing the wiring from VmBakeCmd.Run fails this test.
func TestBakeUsesTheDriveCloneWiring(t *testing.T) {
	vs := &VmSpec{}
	applyCloneDriveSource(vs, "cachyos-vm", "golden")
	if vs.Source.Kind != "clone" || vs.Source.FromVm != "cachyos-vm" || vs.Source.FromSnapshot != "golden" {
		t.Errorf("bake source = kind=%q from_vm=%q from_snapshot=%q, want clone/cachyos-vm/golden",
			vs.Source.Kind, vs.Source.FromVm, vs.Source.FromSnapshot)
	}
}
