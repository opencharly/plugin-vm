package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildContainerDiskRealCuaArtifact pulls the REAL published Cua Fleet image through
// the arm's engine. Gated on LIVE_CUA_ARTIFACT so an offline host skips visibly.
func TestBuildContainerDiskRealCuaArtifact(t *testing.T) {
	ref := os.Getenv("LIVE_CUA_ARTIFACT")
	if ref == "" {
		t.Skip("LIVE_CUA_ARTIFACT unset — skipping the real Cua Fleet pull")
	}
	tmp := t.TempDir()
	outDir := filepath.Join(tmp, "out")
	stateDir := filepath.Join(tmp, "state")
	_ = os.MkdirAll(outDir, 0o755)
	_ = os.MkdirAll(stateDir, 0o755)
	spec := &VmSpec{Source: VmSource{Kind: "container_disk", Image: ref, Cache: filepath.Join(tmp, "cache")}}
	res, err := BuildContainerDisk(spec, outDir, stateDir, nil, true)
	if err != nil {
		t.Fatalf("BuildContainerDisk(real): %v", err)
	}
	if _, err := os.Stat(res.DiskPath); err != nil {
		t.Fatalf("disk missing: %v", err)
	}
	out, _ := exec.Command("qemu-img", "info", res.DiskPath).CombinedOutput()
	t.Logf("REAL Cua Fleet artifact pulled:\n%s", out)
	if !strings.Contains(string(out), "qcow2") {
		t.Errorf("expected a qcow2 guest disk, got: %s", out)
	}
}
