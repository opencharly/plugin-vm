package vm

import (
	"path/filepath"
	"testing"

	"github.com/opencharly/sdk/vmshared"
)

// TestBakeWorkingDisk gates the bake's WORKING-DISK resolution. The bake boots a
// PER-DOMAIN overlay keyed by the bake domain (`<entity>-bake`), NOT the entity's own
// disk: the former self-clone wrote the clone INTO the entity's disk, which is the backing
// file of the entity's own golden snapshot, producing a circular qcow2 backing chain
// (plugin-vm#54). This is the path the bake flattens and emits, so its shape is the
// contract that keeps the bake off the golden's backing.
func TestBakeWorkingDisk(t *testing.T) {
	root := t.TempDir()
	t.Setenv(vmshared.VmStateDirEnv, root)

	bakeDomain := "cua-container-disk-vm" + "-bake"
	got, err := bakeWorkingDisk(bakeDomain)
	if err != nil {
		t.Fatalf("bakeWorkingDisk: %v", err)
	}
	want := filepath.Join(root, "charly-cua-container-disk-vm-bake", "disk.qcow2")
	if got != want {
		t.Errorf("bakeWorkingDisk = %q, want %q", got, want)
	}
	// The working disk must NOT be the entity's own disk dir (the golden's backing).
	entityDisk, err := vmshared.VmDiskDir("cua-container-disk-vm")
	if err != nil {
		t.Fatalf("VmDiskDir: %v", err)
	}
	if filepath.Dir(got) == entityDisk {
		t.Errorf("bake working disk %q is the entity's own disk dir %q — the circular-chain defect", got, entityDisk)
	}
}

// TestBakeDomainName gates the domain resolution: an explicit --domain IS the domain (a
// bed passes its disposable deploy so the bake keys the snapshot registry + boots the
// deploy's own overlay); without one it derives `<entity>-bake`. The domain is what
// LookupSnapshot and every create/stop/destroy use, so a regression here re-opens the
// entity-vs-deploy split the architecture fix closed.
func TestBakeDomainName(t *testing.T) {
	if got := bakeDomainName("omarchy-vm", "check-cua-fleet-build"); got != "check-cua-fleet-build" {
		t.Errorf("bakeDomainName with explicit domain = %q, want check-cua-fleet-build", got)
	}
	if got := bakeDomainName("omarchy-vm", ""); got != "omarchy-vm-bake" {
		t.Errorf("bakeDomainName derived = %q, want omarchy-vm-bake", got)
	}
	// The explicit domain must NOT be the entity itself (that would re-open the defect:
	// the entity's own disk is the golden's backing file).
	if got := bakeDomainName("omarchy-vm", "omarchy-vm"); got == "omarchy-vm-bake" {
		t.Errorf("explicit domain ignored: %q", got)
	}
}

// TestBakeCreateCmd gates the KeepDisk WIRING (not just the domain helper): a bake with an
// explicit --domain MUST create with KeepDisk (the domain's disk is the golden's backing —
// recreating it drops the backing and the bake boots an empty overlay); the derived path
// must NOT. Fails if Run() ignores the assembled cmd (e.g. builds VmCreateCmd inline with
// KeepDisk hardcoded either way).
func TestBakeCreateCmd(t *testing.T) {
	explicit := bakeCreateCmd("omarchy-vm", "cua-container-disk-produce", "cua-container-disk-produce")
	if explicit.Domain != "cua-container-disk-produce" {
		t.Errorf("create domain = %q, want cua-container-disk-produce", explicit.Domain)
	}
	if !explicit.KeepDisk {
		t.Error("a bake with --domain must create KeepDisk:true (reuse the deploy's disk — the golden's backing)")
	}
	derived := bakeCreateCmd("omarchy-vm", "", "omarchy-vm-bake")
	if derived.Domain != "omarchy-vm-bake" {
		t.Errorf("derived create domain = %q, want omarchy-vm-bake", derived.Domain)
	}
	if derived.KeepDisk {
		t.Error("a derived (no --domain) bake must create KeepDisk:false (fresh overlay as before)")
	}
}
