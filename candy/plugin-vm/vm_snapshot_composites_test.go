package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
	"github.com/opencharly/sdk"
)

// --- create-consistent: the Quiesce contract --------------------------

// TestCreateConsistent_QuiesceTrue is the acceptance gate for create-consistent's
// quiesce contract: the command surface MUST pass Quiesce=true into the snapshot
// creation — the registry records Quiesced=true and libvirt's quiesce flag
// re-freezes an already-frozen filesystem as a no-op. FAILS if a future refactor
// drops the flag from consistentCreateOpts (the snapshot would then be recorded
// as non-quiesced and the "consistent" guarantee would be unverifiable).
// TestMakeSnapshotSharedReadOnly is the R1 regression gate for the
// parallel-clone lock regression: an external snapshot disk must be READ-ONLY
// (0444) after capture so every clone VM opens it with a SHARED lock (qemu
// auto-read-only would otherwise try read-write and take an EXCLUSIVE lock,
// blocking parallel clones). The witness: makeSnapshotReadOnly sets 0444;
// makeSnapshotWritable restores 0644 for a re-capture.
func TestMakeSnapshotSharedReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "golden.qcow2")
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("create golden: %v", err)
	}
	if err := makeSnapshotReadOnly(path); err != nil {
		t.Fatalf("makeSnapshotReadOnly: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Fatalf("snapshot disk must be read-only (0444) for shared clone locks; got %o", fi.Mode().Perm())
	}
	if err := makeSnapshotWritable(path); err != nil {
		t.Fatalf("makeSnapshotWritable: %v", err)
	}
	fi, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("re-capture must restore writable (0644); got %o", fi.Mode().Perm())
	}
	// A not-yet-existing file (first capture) is a no-op, not an error.
	if err := makeSnapshotWritable(filepath.Join(dir, "missing.qcow2")); err != nil {
		t.Fatalf("makeSnapshotWritable on a missing file must be a no-op: %v", err)
	}
}

func TestCreateConsistent_QuiesceTrue(t *testing.T) {
	opts := consistentCreateOpts("myvm", "snap1", "", "a consistent snapshot")
	if !opts.Quiesce {
		t.Fatal("create-consistent must pass Quiesce=true into CreateSnapshot — got Quiesce=false")
	}
	if opts.Mode != "external" {
		t.Fatalf("create-consistent defaults to external mode, got %q", opts.Mode)
	}
	if opts.VmName != "myvm" || opts.SnapName != "snap1" {
		t.Fatalf("opts carry the wrong identity: %+v", opts)
	}
}

// TestCreateConsistent_ValidatesArgs proves the composite rejects an empty
// identity before touching the engine.
func TestCreateConsistent_ValidatesArgs(t *testing.T) {
	if _, err := createConsistentSnapshot(SnapshotCreateOpts{VmName: "", SnapName: "s"}); err == nil {
		t.Fatal("empty vm name must be rejected")
	}
	if _, err := createConsistentSnapshot(SnapshotCreateOpts{VmName: "v", SnapName: ""}); err == nil {
		t.Fatal("empty snapshot name must be rejected")
	}
}

// TestCreateConsistent_StrictAgentOnRunningDomain proves the STRICT quiesce
// precondition on a real running domain WITHOUT a guest agent: the composite
// must FAIL with the agent-unreachable error — never silently fall back to a
// non-quiesced snapshot (the `create --quiesce` fallback path would SUCCEED
// here with a stderr note; create-consistent must not). FAILS if the strict
// agent precondition is removed.
// Gated behind -short (needs qemu:///session + /dev/kvm).
func TestCreateConsistent_StrictAgentOnRunningDomain(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real libvirt domain (needs qemu:///session + /dev/kvm)")
	}
	conn, err := connectLibvirt("")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	const entity = "test-consistent-strict"
	// Literal, not computed: `const xml` below must stay a constant expression.
	const domName = "charly-test-consistent-strict"
	const xml = `<domain type='kvm'>
  <name>` + domName + `</name>
  <memory unit='MiB'>128</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices>
</domain>`
	cleanup := func() {
		if d, e := conn.lookupDomain(domName); e == nil {
			_ = conn.destroyDomain(d)
			_ = conn.undefineDomain(d, true)
		}
	}
	cleanup()
	defer cleanup()

	dom, err := conn.l.DomainDefineXML(xml)
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := conn.startDomain(dom); err != nil {
		t.Fatalf("start: %v", err)
	}
	if st, _ := conn.domainState(dom); st != libvirt.DomainRunning {
		t.Fatalf("precondition: domain must be running (state=%v)", st)
	}

	_, err = createConsistentSnapshot(SnapshotCreateOpts{VmName: entity, SnapName: "s1", Mode: "external"})
	if err == nil || !strings.Contains(err.Error(), "qemu-guest-agent unreachable") {
		t.Fatalf("create-consistent on a running domain WITHOUT a guest agent must FAIL the strict agent precondition (never silently fall back to a non-quiesced snapshot), got: %v", err)
	}
}

// TestCreateConsistent_InternalModeRefusesRunningDomain proves the honest
// internal-mode semantics: qemu-img cannot mutate a live qcow2, so a consistent
// INTERNAL snapshot of a RUNNING VM is a hard error — never a silent
// non-consistent snapshot.
// Gated behind -short (needs qemu:///session + /dev/kvm).
func TestCreateConsistent_InternalModeRefusesRunningDomain(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real libvirt domain (needs qemu:///session + /dev/kvm)")
	}
	conn, err := connectLibvirt("")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	const entity = "test-consistent-internal-refused"
	// Literal, not computed: `const xml` below must stay a constant expression.
	const domName = "charly-test-consistent-internal-refused"
	const xml = `<domain type='kvm'>
  <name>` + domName + `</name>
  <memory unit='MiB'>128</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <devices><emulator>/usr/bin/qemu-system-x86_64</emulator></devices>
</domain>`
	cleanup := func() {
		if d, e := conn.lookupDomain(domName); e == nil {
			_ = conn.destroyDomain(d)
			_ = conn.undefineDomain(d, true)
		}
	}
	cleanup()
	defer cleanup()

	dom, err := conn.l.DomainDefineXML(xml)
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := conn.startDomain(dom); err != nil {
		t.Fatalf("start: %v", err)
	}

	_, err = createConsistentSnapshot(SnapshotCreateOpts{VmName: entity, SnapName: "s1", Mode: "internal"})
	if err == nil || !strings.Contains(err.Error(), "requires the VM stopped") {
		t.Fatalf("internal-mode create-consistent on a RUNNING VM must be a hard error, got: %v", err)
	}
}

// --- revert-and-start: the stop-before-revert order contract ------------

// TestRevertAndStart_StopsBeforeRevertThenStarts is the ordering acceptance test:
// the composite MUST stop the domain BEFORE reverting (revert requires the domain
// offline — qemu-img refuses a live qcow2, libvirt external revert needs the
// domain shut off) and start AFTER the revert succeeded. FAILS if the order is
// changed or any step is dropped.
func TestRevertAndStart_StopsBeforeRevertThenStarts(t *testing.T) {
	var calls []string
	steps := revertAndStartSteps{
		stop: func(name string, force bool) (bool, error) {
			calls = append(calls, "stop:"+name)
			return true, nil
		},
		revert: func(vm, snap string) error {
			calls = append(calls, "revert:"+vm+":"+snap)
			return nil
		},
		start: func(name string) error {
			calls = append(calls, "start:"+name)
			return nil
		},
	}
	if err := steps.run("myvm", "snap1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"stop:charly-myvm", "revert:myvm:snap1", "start:charly-myvm"}
	if !slices.Equal(calls, want) {
		t.Fatalf("revert-and-start order: got %v, want %v — the stop must precede the revert (offline-domain requirement) and the start must follow the revert", calls, want)
	}
}

// TestRevertAndStart_StopFailureAborts: a failed stop must abort — no revert,
// no start (a revert on a still-live domain is exactly the qemu-img/lock failure
// the composite exists to prevent).
func TestRevertAndStart_StopFailureAborts(t *testing.T) {
	var calls []string
	steps := revertAndStartSteps{
		stop: func(name string, force bool) (bool, error) {
			calls = append(calls, "stop")
			return false, fmt.Errorf("stop exploded")
		},
		revert: func(vm, snap string) error {
			calls = append(calls, "revert")
			return nil
		},
		start: func(name string) error {
			calls = append(calls, "start")
			return nil
		},
	}
	err := steps.run("myvm", "snap1")
	if err == nil || !strings.Contains(err.Error(), "stop exploded") {
		t.Fatalf("stop failure must propagate, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("a failed stop must abort the composite (no revert/start), got calls %v", calls)
	}
}

// TestRevertAndStart_RevertFailureAborts: a failed revert must propagate and
// must NOT start the VM (starting after a failed revert would boot the wrong
// state).
func TestRevertAndStart_RevertFailureAborts(t *testing.T) {
	var calls []string
	steps := revertAndStartSteps{
		stop: func(name string, force bool) (bool, error) {
			calls = append(calls, "stop")
			return true, nil
		},
		revert: func(vm, snap string) error {
			calls = append(calls, "revert")
			return fmt.Errorf("revert exploded")
		},
		start: func(name string) error {
			calls = append(calls, "start")
			return nil
		},
	}
	err := steps.run("myvm", "snap1")
	if err == nil || !strings.Contains(err.Error(), "revert exploded") {
		t.Fatalf("revert failure must propagate, got %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("a failed revert must NOT start the VM, got calls %v", calls)
	}
}

// TestRevertAndStart_RejectsMissingVM: an absent VM (no libvirt domain, no qemu
// state) must FAIL — the #77/#69 false-success class — never proceed to revert.
func TestRevertAndStart_RejectsMissingVM(t *testing.T) {
	var calls []string
	steps := revertAndStartSteps{
		stop: func(name string, force bool) (bool, error) {
			calls = append(calls, "stop")
			return false, nil // VM absent — the authoritative probe found nothing
		},
		revert: func(vm, snap string) error {
			calls = append(calls, "revert")
			return nil
		},
		start: func(name string) error {
			calls = append(calls, "start")
			return nil
		},
	}
	err := steps.run("myvm", "snap1")
	if err == nil || !strings.Contains(err.Error(), "no such VM") {
		t.Fatalf("an absent VM must fail with 'no such VM', got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("an absent VM must abort before revert/start, got calls %v", calls)
	}
}

// TestRevertAndStart_ValidatesArgs proves the composite rejects an empty
// identity before touching the engine.
func TestRevertAndStart_ValidatesArgs(t *testing.T) {
	if err := (revertAndStartSteps{}).run("", "snap1"); err == nil {
		t.Fatal("empty vm name must be rejected")
	}
	if err := (revertAndStartSteps{}).run("myvm", ""); err == nil {
		t.Fatal("empty snapshot name must be rejected")
	}
}

// --- kong wiring: the composite verbs must parse -------------------------

// TestVmSnapshotCmd_CompositeSubcommandsWired proves the kong grammar registers
// the two §5.3 composite verbs: `snapshot create-consistent <vm> <name>` and
// `snapshot revert-and-start <vm> <name>` must PARSE into the VmCmd tree (a dropped
// `cmd:""` wiring would surface here as an unknown-command parse error). Parse
// only — nothing runs.
func TestVmSnapshotCmd_CompositeSubcommandsWired(t *testing.T) {
	for _, args := range [][]string{
		{"snapshot", "create-consistent", "myvm", "snap1"},
		{"snapshot", "create-consistent", "myvm", "snap1", "--mode", "internal"},
		{"snapshot", "revert-and-start", "myvm", "snap1"},
	} {
		if done, err := sdk.ParseInProcCLI("vm", &VmCmd{}, args); err != nil {
			t.Fatalf("kong must parse %v: %v", args, err)
		} else if done {
			t.Fatalf("kong reported done for %v (help/version?)", args)
		}
	}
}

// TestSnapshotVmName_DomainOverride pins the --domain resolver: a non-empty
// --domain (a check bed's per-deploy name) replaces the entity name for ALL
// snapshot purposes (registry, disk paths, domain lookup); empty falls back to
// the entity. FAILS if a refactor drops the override (the snapshot-anchored
// check-run mode would then target the entity's own domain, which does not
// exist for a bed — #33/P33).
func TestSnapshotVmName_DomainOverride(t *testing.T) {
	if got := snapshotVmName("omarchy-vm", "check-omarchy-desktop-vm"); got != "check-omarchy-desktop-vm" {
		t.Fatalf("snapshotVmName(entity, domain) = %q, want the deploy name (the bed's domain identity)", got)
	}
	if got := snapshotVmName("omarchy-vm", ""); got != "omarchy-vm" {
		t.Fatalf("snapshotVmName(entity, empty) = %q, want the entity (no --domain)", got)
	}
}

// --- revert-and-start: real-libvirt offline-revert + restart -------------

// TestRevertAndStart_OfflineRevertThenRestart drives the REAL composite
// (revertAndStartVm bound to the real stopVmDomain / RevertSnapshot /
// startVmDomain) against a real libvirt domain + a real qcow2 holding an
// internal snapshot: the domain is defined-but-shutoff (the offline state the
// composite guarantees), the revert runs qemu-img against the now-unlocked
// disk, and the start brings the domain back to RUNNING. FAILS if the composite
// fails to leave the domain offline for the revert (qemu-img refuses a live
// qcow2) or fails to restart it.
// Gated behind -short (needs qemu:///session + /dev/kvm).
func TestRevertAndStart_OfflineRevertThenRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("creates a real libvirt domain (needs qemu:///session + /dev/kvm)")
	}
	const entity = "test-revert-and-start"
	const snapName = "snap1"
	domName := "charly-" + entity

	// Seed the VM state dir with a real qcow2 holding an internal snapshot + a
	// registry entry (the exact layout vmDiskPath / snapshotsDir resolve for
	// this entity under the REAL vm state root — see vm_stop_test.go).
	dir, err := vmDir()
	if err != nil {
		t.Fatalf("vmDir: %v", err)
	}
	stateDir := filepath.Join(dir, domName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("seed state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })

	disk := filepath.Join(stateDir, "disk.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", disk, "64M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	if out, err := exec.Command("qemu-img", "snapshot", "-c", snapName, disk).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img snapshot -c: %v (%s)", err, out)
	}
	regDir := filepath.Join(stateDir, "snapshots")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshots: %v", err)
	}
	reg := map[string]any{
		"version": 1,
		"snapshots": map[string]any{
			snapName: map[string]any{
				"name":         snapName,
				"mode":         "internal",
				"libvirt_name": snapName,
				"created":      "2026-01-01T00:00:00Z",
				"refcount":     0,
			},
		},
	}
	regJSON, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.json"), regJSON, 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	// Define + start a real domain with the seeded disk attached, then stop it
	// (shutoff) — the state the composite's own stop leaves.
	conn, err := connectLibvirt("")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	xmlStr := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='MiB'>128</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>`, domName, disk)
	cleanup := func() {
		if d, e := conn.lookupDomain(domName); e == nil {
			_ = conn.destroyDomain(d)
			_ = conn.undefineDomain(d, true)
		}
	}
	cleanup() // clear a leftover from an interrupted earlier run
	defer cleanup()

	dom, err := conn.l.DomainDefineXML(xmlStr)
	if err != nil {
		t.Fatalf("define: %v", err)
	}
	if err := conn.startDomain(dom); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := conn.destroyDomain(dom); err != nil {
		t.Fatalf("pre-stop (destroy): %v", err)
	}
	if st, _ := conn.domainState(dom); st != libvirt.DomainShutoff {
		t.Fatalf("precondition: domain must be shutoff (state=%v)", st)
	}

	// The composite under test: stop (no-op: already off) -> revert (qemu-img on
	// the offline disk) -> start (domain back up).
	if err := revertAndStartVm(entity, snapName); err != nil {
		t.Fatalf("revertAndStartVm: %v", err)
	}
	if st, serr := conn.domainState(dom); serr != nil || st != libvirt.DomainRunning {
		t.Fatalf("after revert-and-start the domain must be RUNNING (state=%v err=%v)", st, serr)
	}
	// The internal snapshot must survive the revert+restart cycle. qemu-img
	// cannot list a disk a RUNNING qemu holds (lock conflict), so stop the
	// domain again before the -l check (cleanup would destroy it anyway).
	if err := conn.destroyDomain(dom); err != nil {
		t.Fatalf("re-stop for snapshot check: %v", err)
	}
	out, err := exec.Command("qemu-img", "snapshot", "-l", disk).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img snapshot -l: %v", err)
	}
	if !strings.Contains(string(out), snapName) {
		t.Fatalf("internal snapshot %q must still exist after revert-and-start, qemu-img -l says: %s", snapName, out)
	}
}
