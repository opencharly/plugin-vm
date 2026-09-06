package vm

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestDiskIsGoldenClone gates the from:name:tag clone default (the vm-create iso re-pack
// skip): a disk WITH a backing file (the vm-build drive's golden clone) reports true; a
// plain disk (no backing) reports false; a missing disk degrades to false (the iso re-pack
// proceeds — the pre-clone behavior).
func TestDiskIsGoldenClone(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	clone := filepath.Join(dir, "clone.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", base, "1M").CombinedOutput(); err != nil {
		t.Fatalf("creating the base disk: %v (%s)", err, out)
	}
	if err := qemuImgCreateOverlay(base, clone); err != nil {
		t.Fatalf("creating the probe overlay: %v", err)
	}
	if !diskIsGoldenClone(clone) {
		t.Fatal("diskIsGoldenClone(clone) = false, want true (a backing file marks the golden clone)")
	}
	if diskIsGoldenClone(base) {
		t.Fatal("diskIsGoldenClone(base) = true, want false (a plain disk has no backing)")
	}
	if diskIsGoldenClone(filepath.Join(dir, "missing.qcow2")) {
		t.Fatal("diskIsGoldenClone(missing) = true, want false (a probe failure degrades to not-a-clone)")
	}
}
