package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/opencharly/sdk/vmshared"
)

// TestShouldRepackIsoAnswersSkipsGoldenClone gates the golden-clone iso re-pack skip
// (B12): shouldRepackIsoAnswers with a golden-clone disk (a disk with a backing file) +
// an iso source + perDomain + a seed path must return FALSE (the re-pack is skipped).
// Removing the && !diskIsGoldenClone(baseQcow2) clause from shouldRepackIsoAnswers makes
// this case return TRUE -> this test FAILS.
func TestShouldRepackIsoAnswersSkipsGoldenClone(t *testing.T) {
	diskDir, err := filepath.Abs(vmshared.VmDiskDir("test-entity"))
	if err != nil {
		t.Fatalf("abs disk dir: %v", err)
	}
	if err := os.MkdirAll(diskDir, 0o755); err != nil {
		t.Fatalf("mkdir disk dir: %v", err)
	}
	base := filepath.Join(diskDir, "base.qcow2")
	clone := filepath.Join(diskDir, "disk.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", base, "1M").CombinedOutput(); err != nil {
		t.Fatalf("create base: %v (%s)", err, out)
	}
	if err := qemuImgCreateOverlay(base, clone); err != nil {
		t.Fatalf("create golden-clone overlay: %v", err)
	}
	spec := &VmSpec{Source: VmSource{Kind: "iso"}}
	if shouldRepackIsoAnswers(spec, true, "/some/seed.iso", clone) {
		t.Fatal("golden clone must SKIP the iso answers re-pack")
	}
	// Control: a plain (non-clone) disk must re-pack.
	plain := filepath.Join(diskDir, "plain.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", plain, "1M").CombinedOutput(); err != nil {
		t.Fatalf("create plain disk: %v (%s)", err, out)
	}
	if !shouldRepackIsoAnswers(spec, true, "/some/seed.iso", plain) {
		t.Fatal("a plain disk must re-pack the iso answers")
	}
}
