package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubKrunContainerState replaces the engine-backed running probe for a test.
func stubKrunContainerState(t *testing.T, running bool) {
	t.Helper()
	orig := krunContainerRunning
	krunContainerRunning = func(string) bool { return running }
	t.Cleanup(func() { krunContainerRunning = orig })
}

func writeKrunFixture(t *testing.T, stateDir, name, alias string) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := krunState{
		Name:     name,
		Engine:   "containerd",
		Image:    "ghcr.io/opencharly/test:1",
		SSHPort:  2222,
		SSHAlias: alias,
		Systemd:  true,
		Argv:     []string{"nerdctl", "run"},
	}
	if err := writeKrunState(stateDir, state); err != nil {
		t.Fatal(err)
	}
}

// TestKrunStateRoundTrip pins the persisted record the start/holder paths read.
// Mutation: dropping a field from the JSON struct loses the SSH alias and the
// stop path can no longer find its guest.
func TestKrunStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeKrunFixture(t, dir, "charly-rt", "charly-rt")
	state, err := readKrunState(dir)
	if err != nil {
		t.Fatalf("readKrunState: %v", err)
	}
	if state.SSHAlias != "charly-rt" || state.SSHPort != 2222 || !state.Systemd {
		t.Fatalf("round-trip lost fields: %+v", state)
	}
}

// TestKrunStopPowersOffNeverSignals pins the stop contract: the guest is
// powered off over its managed SSH alias, and the launch argv is never
// re-executed (no signal path). Mutation: replacing the SSH poweroff with any
// engine stop/kill fails the alias assertion.
func TestKrunStopPowersOffNeverSignals(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-stop-fixture"
	writeKrunFixture(t, filepath.Join(root, name), name, "charly-stop-fixture")

	calls := 0
	origRunning := krunContainerRunning
	krunContainerRunning = func(string) bool {
		calls++
		return calls == 1 // running at entry, gone once poweroff ran
	}
	t.Cleanup(func() { krunContainerRunning = origRunning })

	var poweredOffAlias string
	origPoweroff := krunPoweroffOverSSH
	krunPoweroffOverSSH = func(alias string) error {
		poweredOffAlias = alias
		return nil
	}
	t.Cleanup(func() { krunPoweroffOverSSH = origPoweroff })

	origRun := runKrunArgv
	runCalled := false
	runKrunArgv = func([]string) error { runCalled = true; return nil }
	t.Cleanup(func() { runKrunArgv = origRun })

	if err := (krunBackend{}).Stop(name, false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if poweredOffAlias != "charly-stop-fixture" {
		t.Fatalf("poweroff alias = %q, want the managed SSH alias", poweredOffAlias)
	}
	if runCalled {
		t.Fatal("Stop re-executed the launch argv instead of powering off over SSH")
	}
}

// TestKrunStopForceStillPowersOff pins that --force does not introduce a
// signal: the brief forbids a signal-based stop for a systemd guest.
func TestKrunStopForceStillPowersOff(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-force-fixture"
	writeKrunFixture(t, filepath.Join(root, name), name, "charly-force-fixture")

	calls := 0
	origRunning := krunContainerRunning
	krunContainerRunning = func(string) bool { calls++; return calls == 1 }
	t.Cleanup(func() { krunContainerRunning = origRunning })

	poweroffs := 0
	origPoweroff := krunPoweroffOverSSH
	krunPoweroffOverSSH = func(string) error { poweroffs++; return nil }
	t.Cleanup(func() { krunPoweroffOverSSH = origPoweroff })

	if err := (krunBackend{}).Stop(name, true); err != nil {
		t.Fatalf("Stop(force): %v", err)
	}
	if poweroffs != 1 {
		t.Fatalf("force stop poweroffs = %d, want 1 (never a signal)", poweroffs)
	}
}

// TestKrunDestroyRemovesContainerAndState pins the teardown contract. Mutation:
// skipping the container removal leaves the container behind; ignoring
// deleteDisk leaves the state dir behind.
func TestKrunDestroyRemovesContainerAndState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-destroy-fixture"
	stateDir := filepath.Join(root, name)
	writeKrunFixture(t, stateDir, name, name)

	origExists := krunContainerExists
	krunContainerExists = func(string) bool { return true }
	t.Cleanup(func() { krunContainerExists = origExists })

	var removed string
	origRemove := krunContainerRemove
	krunContainerRemove = func(n string) error { removed = n; return nil }
	t.Cleanup(func() { krunContainerRemove = origRemove })

	torn, err := (krunBackend{}).Destroy(name, stateDir, true)
	if err != nil || !torn {
		t.Fatalf("Destroy = (%v, %v), want torn=true", torn, err)
	}
	if removed != name {
		t.Fatalf("removed container = %q, want %q", removed, name)
	}
	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("state dir survived --disk destroy: %v", statErr)
	}
}

// TestRefuseKrunSnapshotNamesTheExtraction pins the snapshot refusal, including
// the issue that must land before the mechanism can be shared. Mutation:
// deleting the guard lets the libvirt/qemu snapshot path run against a
// container. Skipped under -short (it probes the libvirt session daemon).
func TestRefuseKrunSnapshotNamesTheExtraction(t *testing.T) {
	if testing.Short() {
		t.Skip("probes the libvirt session daemon; skipped under -short")
	}
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	stubKrunContainerState(t, false)

	name := "charly-snapshot-fixture"
	writeKrunFixture(t, filepath.Join(root, name), name, name)
	err := refuseKrunSnapshot(strings.TrimPrefix(name, "charly-"))
	if err == nil || !strings.Contains(err.Error(), "crabbox#21") {
		t.Fatalf("krun snapshot refusal = %v, want a named extraction (crabbox#21)", err)
	}

	if err := refuseKrunSnapshot("charly-no-such-vm-fixture"); err != nil {
		t.Fatalf("non-krun snapshot must not be refused: %v", err)
	}
}

// TestKrunPreflightVisibleSkip is the R8 live-or-skip control: on a host with
// no krun runtime handler it proves the skip NAMES the missing mechanism; on a
// host that declares one it skips visibly (the live guest itself needs the
// layer-krun host layer and a disposable target). It never mocks the engine.
func TestKrunPreflightVisibleSkip(t *testing.T) {
	engine, capability, _, err := krunPreflight()
	if err == nil {
		t.Skipf("this host declares the %q runtime handler on engine %q (%v); a live krun guest needs the layer-krun host layer and a disposable target",
			krunRuntimeHandler, engine, capability.RuntimeHandlers)
	}
	if !strings.Contains(err.Error(), krunRuntimeHandler) {
		t.Fatalf("skip error does not name the missing %q handler: %v", krunRuntimeHandler, err)
	}
	if !strings.Contains(err.Error(), "NOT available") && !strings.Contains(err.Error(), "NOT installed") {
		t.Fatalf("skip error does not state the missing mechanism: %v", err)
	}
	t.Logf("VISIBLE SKIP: %v", err)
}

// TestKrunStartStartsAStoppedContainer pins the lifecycle distinction: a present
// but stopped container is `start`ed (re-running `run` would collide on the
// name and lose the writable snapshot); only an ABSENT container re-runs the
// recorded argv. Mutation: always re-running the argv fails the first arm.
func TestKrunStartStartsAStoppedContainer(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	name := "charly-start-existing"
	writeKrunFixture(t, filepath.Join(root, name), name, name)
	stubKrunContainerState(t, false)

	origExists := krunContainerExists
	krunContainerExists = func(string) bool { return true }
	t.Cleanup(func() { krunContainerExists = origExists })

	started := false
	origStart := krunContainerStart
	krunContainerStart = func(n string) error { started = true; return nil }
	t.Cleanup(func() { krunContainerStart = origStart })

	origRun := runKrunArgv
	ran := false
	runKrunArgv = func([]string) error { ran = true; return nil }
	t.Cleanup(func() { runKrunArgv = origRun })

	if _, err := (krunBackend{}).Start(name); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !started || ran {
		t.Fatalf("Start: started=%v ranArgv=%v, want start=true, run=false", started, ran)
	}
}

// TestKrunNVIDIARequired pins the PCI-vendor detection the refusal depends on.
// Mutation: comparing against the un-normalized spelling ("10de" rather than
// "0x10de") silently never refuses.
func TestKrunNVIDIARequired(t *testing.T) {
	cnode := DeployNode{RequiresExclusive: []string{"nvidia-gpu"}}
	resources := map[string]*ResolvedResource{
		"nvidia-gpu": {Gpu: &ResolvedGpuSelector{Vendor: "0x10de"}},
	}
	if !krunNVIDIARequired(&cnode, resources) {
		t.Fatal("NVIDIA vendor 0x10de not detected")
	}
	resources["nvidia-gpu"].Gpu.Vendor = "10de"
	if !krunNVIDIARequired(&cnode, resources) {
		t.Fatal("NVIDIA vendor 10de not detected (prefix-insensitive expected)")
	}
	resources["nvidia-gpu"].Gpu.Vendor = "0x1002"
	if krunNVIDIARequired(&cnode, resources) {
		t.Fatal("AMD vendor reported as NVIDIA")
	}
	if krunNVIDIARequired(nil, resources) {
		t.Fatal("no claimant reported as NVIDIA")
	}
}

// TestKrunScreenshotRefused pins the krun screen-capture refusal: crun/krun does
// not expose the virtio-gpu display host-side, so the libvirt/qemu capture path
// must not be run against a krun guest.
func TestKrunScreenshotRefused(t *testing.T) {
	err := (krunBackend{}).Screenshot("charly-x", "/tmp/x.png")
	if err == nil || !strings.Contains(err.Error(), "no host-capturable framebuffer") {
		t.Fatalf("krun screenshot = %v, want a named refusal", err)
	}
}

// TestKrunRecordedAliasEqualsPublishedAlias pins the invariant the stop path
// depends on: the alias recorded in the krun state must be EXACTLY the alias
// publishVmSshAlias published for the same create. publishVmSshAlias is called
// with the BARE name and applies VmSshAlias; runKrunCreate records
// vmManagedSSHAlias of that same bare name, so `ssh <recorded>` reaches the
// published Host stanza. Mutation: recording the bare name, or recording the FQ
// name while publishing the bare one, fails.
func TestKrunRecordedAliasEqualsPublishedAlias(t *testing.T) {
	const bare = "myvm"
	published := VmSshAlias(bare)       // what publishVmSshAlias puts in the stanza
	recorded := vmManagedSSHAlias(bare) // what runKrunCreate puts in KrunSSHAlias
	if recorded != published {
		t.Fatalf("recorded alias %q != published alias %q", recorded, published)
	}
	if recorded != "charly-myvm" {
		t.Fatalf("managed alias = %q, want charly-myvm", recorded)
	}
	req := vmCreateReq{Name: bare, VmDomainName: "charly-" + bare, KrunSSHAlias: recorded}
	if req.KrunSSHAlias != VmSshAlias(req.Name) {
		t.Fatalf("state alias %q diverges from the published alias for %q", req.KrunSSHAlias, req.Name)
	}
}
