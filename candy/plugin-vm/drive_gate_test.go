package vm

import (
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestVmBuildDriveSourceKind gates the from: name:tag DRIVE decision: a request carrying
// from_snapshot dispatches SourceKind "clone" (the drive, not an entity kind); without it,
// the entity's own kind wins. Removing the drive branch in resolveVmBuild fails this.
func TestVmBuildDriveSourceKind(t *testing.T) {
	if got := vmBuildDriveSourceKind("cloud_image", spec.VmBuildRequest{Box: "x", FromSnapshot: "golden"}); got != "clone" {
		t.Errorf("with from_snapshot: want drive kind clone, got %q", got)
	}
	if got := vmBuildDriveSourceKind("iso", spec.VmBuildRequest{Box: "x"}); got != "iso" {
		t.Errorf("without from_snapshot: want the entity kind iso, got %q", got)
	}
}

// TestApplyCloneDriveSource gates the drive's source wiring: BuildClone requires
// source.kind == clone + FromVm + FromSnapshot; the drive fills all three from the
// request (from_vm = the entity itself). Removing the wiring in vm_build.go fails this.
func TestApplyCloneDriveSource(t *testing.T) {
	vs := &VmSpec{}
	applyCloneDriveSource(vs, "cachyos-vm", "golden")
	if vs.Source.Kind != "clone" || vs.Source.FromVm != "cachyos-vm" || vs.Source.FromSnapshot != "golden" {
		t.Errorf("drive source = kind=%q from_vm=%q from_snapshot=%q, want clone/cachyos-vm/golden",
			vs.Source.Kind, vs.Source.FromVm, vs.Source.FromSnapshot)
	}
	if err := BuildClone("cachyos-vm", vs, "", "/tmp"); err == nil {
		t.Errorf("BuildClone with the drive source must reach the input guards (missing snapshot on disk fails there)")
	}
}
