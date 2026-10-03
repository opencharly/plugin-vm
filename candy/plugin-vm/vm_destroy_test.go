package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingDestroyPolicy(t *testing.T) {
	if err := missingDestroyError("charly-typo", false); err == nil {
		t.Fatal("strict operator destroy accepted a missing VM")
	}
	if err := missingDestroyError("charly-reconciled", true); err != nil {
		t.Fatalf("--if-exists rejected an already-absent VM: %v", err)
	}
}

// TestDestroyVmDomain_NotFound proves the #69 regression fix: a name with NO libvirt domain AND NO
// qemu state dir reports torn=false with no error (which VmDestroyCmd.Run turns into a hard "no such
// VM" non-zero exit) — never a false "Destroyed VM" success. Runs under the REAL HOME on purpose: a
// temp HOME would make the libvirt probe autospawn the session daemon rooted at a dir that vanishes
// when the test ends, breaking every later libvirt test in the process (a real isolation trap). The
// name is one no domain/state dir can match.
func TestDestroyVmDomain_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the libvirt session daemon; skipped under -short")
	}
	torn, err := destroyVmDomain("charly-vm69-regression-nonexistent-do-not-create", false)
	if err != nil {
		t.Fatalf("destroyVmDomain(nonexistent): unexpected error: %v", err)
	}
	if torn {
		t.Fatalf("destroyVmDomain(nonexistent): torn=true — a missing VM must report NOT torn (the #69 false-success class)")
	}
}

// TestDestroyVmDomain_QemuStateDir proves the qemu arm tears the VM down and reports torn=true,
// removing the per-VM state dir. No running qemu process is needed — force-shutdown falls through to
// the state-dir removal. The libvirt probe misses the random name and falls to the qemu path. Seeds a
// uniquely-named dir under the REAL vm state dir (not a temp HOME — see TestDestroyVmDomain_NotFound)
// and cleans it up.
func TestDestroyVmDomain_QemuStateDir(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the libvirt session daemon; skipped under -short")
	}
	dir, err := vmDir()
	if err != nil {
		t.Fatalf("vmDir: %v", err)
	}
	name := "charly-vm69-regression-qemu-fixture"
	stateDir := filepath.Join(dir, name)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("seed state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })

	torn, err := destroyVmDomain(name, false)
	if err != nil {
		t.Fatalf("destroyVmDomain(qemu): unexpected error: %v", err)
	}
	if !torn {
		t.Fatalf("destroyVmDomain(qemu): torn=false — a present qemu state dir must be torn down")
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("destroyVmDomain(qemu): state dir %s still present after teardown", stateDir)
	}
}

// TestRemoveEntityBaseDisk is the #65 guard: a DIRECT entity destroy (domain=="")
// reclaims image/<entity>/ (disk.qcow2 + seed.iso), but a --domain destroy must NOT —
// that dir is the shared read-only BASE every per-deploy overlay backs onto, so removing
// it from the deploy path corrupts every sibling domain. Hermetic (CHARLY_VM_IMAGE_DIR
// pins an absolute temp root — no cwd/config dependency).
func TestRemoveEntityBaseDisk(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_IMAGE_DIR", root)
	entity := "probe-entity"
	base := filepath.Join(root, entity)
	seed := func() {
		if err := os.MkdirAll(base, 0o755); err != nil {
			t.Fatalf("seed base dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(base, "disk.qcow2"), []byte("base"), 0o644); err != nil {
			t.Fatalf("seed base disk: %v", err)
		}
	}

	// --domain destroy: the shared base MUST survive (the #65 fix).
	seed()
	removed, err := removeEntityBaseDisk(entity, "check-some-bed")
	if err != nil {
		t.Fatalf("removeEntityBaseDisk(--domain): %v", err)
	}
	if removed != "" {
		t.Fatalf("removeEntityBaseDisk(--domain) reported removing %q — must not touch the shared base", removed)
	}
	if _, statErr := os.Stat(filepath.Join(base, "disk.qcow2")); statErr != nil {
		t.Fatalf("--domain destroy deleted the shared entity base (plugin-vm#65): %v", statErr)
	}

	// Direct entity destroy: the base IS reclaimed.
	removed, err = removeEntityBaseDisk(entity, "")
	if err != nil {
		t.Fatalf("removeEntityBaseDisk(direct): %v", err)
	}
	if removed == "" {
		t.Fatal("removeEntityBaseDisk(direct) did not report a removed dir")
	}
	if _, statErr := os.Stat(base); !os.IsNotExist(statErr) {
		t.Fatalf("direct entity destroy left the base behind: %v", statErr)
	}
}

// TestRemoveVmStateDir guards the ONE shared per-domain state-dir resolver+removal the libvirt and
// qemu destroy arms both use (R3). Hermetic — no libvirt, no HOME dependency — so it always runs.
func TestRemoveVmStateDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-remove-state-dir-fixture"
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o755); err != nil {
		t.Fatalf("seed state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), []byte("overlay"), 0o644); err != nil {
		t.Fatalf("seed overlay: %v", err)
	}
	if err := removeVmStateDir(name); err != nil {
		t.Fatalf("removeVmStateDir: %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("removeVmStateDir left %s behind: %v", dir, statErr)
	}
}

// TestDestroyVmDomain_LibvirtDiskRemovesStateDir is the #64 regression guard: the libvirt destroy arm
// with --disk (deleteDisk=true) MUST reclaim the per-domain HOST state dir, not only the repo
// image/<entity>/ dir. Before the fix the arm returned without touching it, so `vm destroy --disk`
// printed success while orphaning the live overlay (up to tens of GB), reclaimed only by a SECOND
// identical destroy via the qemu fallback. The libvirt destroy op is idempotent on a missing domain,
// so a uniquely-named fixture exercises the real arm without a live domain. Skipped under -short (the
// op probes the libvirt session daemon, like the sibling tests).
func TestDestroyVmDomain_LibvirtDiskRemovesStateDir(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the libvirt session daemon; skipped under -short")
	}
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-vm64-regression-libvirt-disk-fixture"
	stateDir := filepath.Join(root, name)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("seed state dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "disk.qcow2"), []byte("overlay"), 0o644); err != nil {
		t.Fatalf("seed overlay: %v", err)
	}
	torn, err := destroyVmDomainOnBackend(name, "libvirt", "", true)
	if err != nil {
		t.Fatalf("destroyVmDomainOnBackend(libvirt, --disk): unexpected error: %v", err)
	}
	if !torn {
		t.Fatal("destroyVmDomainOnBackend(libvirt, --disk): torn=false — a probed backend must report torn")
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("libvirt --disk left the per-domain state dir behind (plugin-vm#64): %s", stateDir)
	}
}

// TestDestroyVmDomain_LibvirtKeepDiskPreservesStateDir proves the fix is gated on --disk: destroying
// a libvirt VM WITHOUT --disk must NOT touch the state dir (--keep-disk semantics — the domain's disk
// can be a golden snapshot's backing). Skipped under -short like its sibling.
func TestDestroyVmDomain_LibvirtKeepDiskPreservesStateDir(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the libvirt session daemon; skipped under -short")
	}
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-vm64-regression-libvirt-keepdisk-fixture"
	stateDir := filepath.Join(root, name)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("seed state dir: %v", err)
	}
	torn, err := destroyVmDomainOnBackend(name, "libvirt", "", false)
	if err != nil {
		t.Fatalf("destroyVmDomainOnBackend(libvirt, no --disk): unexpected error: %v", err)
	}
	if !torn {
		t.Fatal("destroyVmDomainOnBackend(libvirt, no --disk): torn=false — a probed backend must report torn")
	}
	if _, statErr := os.Stat(stateDir); statErr != nil {
		t.Fatalf("libvirt destroy WITHOUT --disk removed the state dir — --keep-disk violated: %v", statErr)
	}
}
