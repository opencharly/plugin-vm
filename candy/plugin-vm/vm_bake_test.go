package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/opencharly/sdk/deploykit"
)

// vm_bake_test.go — the layered VM bake (cutover task 6): the pure decisions
// behind the bake command.

func TestSplitCsv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCsv(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("splitCsv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The bake requires --from-snapshot (the entity clone arm is retired — a baked
// base is a clone of the entity's OWN golden at that snapshot). The guard is the
// real bakeRequiresSnapshot the Run path calls, so a removed or weakened guard
// fails the test.
func TestBakeRequiresFromSnapshot(t *testing.T) {
	if err := bakeRequiresSnapshot(""); err == nil {
		t.Fatal("bakeRequiresSnapshot must refuse an empty --from-snapshot")
	}
	if err := bakeRequiresSnapshot("golden"); err != nil {
		t.Fatalf("bakeRequiresSnapshot must accept a named snapshot, got %v", err)
	}
}

// guestAgentEnableSshArgs is the EXACT ssh subprocess invocation the bake
// runs to enable qemu-guest-agent in the freshly-booted guest (phases 2.5).
// The subprocess itself needs a live guest; the command-construction is what
// this test locks — a changed invocation (alias, flags, command) fails it.
func TestGuestAgentEnableSshArgs(t *testing.T) {
	args := guestAgentEnableSshArgs("charly-clone-vm")
	want := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"charly-clone-vm",
		"sudo", "systemctl", "enable", "--now", "qemu-guest-agent",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("guestAgentEnableSshArgs = %v, want %v", args, want)
	}
}

// bakePollUntil is the bake's bounded-wait primitive (the agent-enable + the
// agent-ping both poll through it). A probe that succeeds must return
// immediately; a probe that keeps failing must time out and surface the last
// error — a removed or un-bounded wait fails these.
func TestBakePollUntil(t *testing.T) {
	calls := 0
	err := bakePollUntil(func() error {
		calls++
		if calls >= 2 {
			return nil
		}
		return fmt.Errorf("not yet")
	}, 2*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("a probe that succeeds on the 2nd call must return nil, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 probe calls, got %d", calls)
	}

	// A probe that never succeeds must time out and surface the last error.
	err = bakePollUntil(func() error { return fmt.Errorf("always failing") }, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("a never-succeeding probe must time out with an error")
	}
}

// TestRunBakePhase5 pins phase 5's flag wiring by driving the REAL phase-5
// function with the emit/push seams stubbed: --container-disk must select the
// KubeVirt/Cua in-image path (deploykit.ContainerDiskPath), and --push must
// deliver the emitted ref. The default (no --container-disk) keeps the VM-box
// path (""), and no --push means no delivery — the coverage that FAILS without
// the flag wiring.
func TestRunBakePhase5(t *testing.T) {
	origEmit, origPush := bakeEmitBox, bakePushBox
	t.Cleanup(func() { bakeEmitBox, bakePushBox = origEmit, origPush })

	entry := &SnapshotEntry{Name: "baked", DiskPath: "/var/lib/charly/disk.qcow2"}
	s := emitFixtureSpec()

	// --container-disk --push: the containerDisk path + a delivery.
	var gotInImage, pushedDst string
	bakeEmitBox = func(_ /*engine*/, _ /*box*/ string, _ *VmSpec, _ /*diskPath*/, inImagePath string) (string, error) {
		gotInImage = inImagePath
		return "localhost/charly-vm:1", nil
	}
	bakePushBox = func(_ /*engine*/, _ /*src*/, dst string) error {
		pushedDst = dst
		return nil
	}
	if err := runBakePhase5("podman", "vm", s, entry, vmBoxEmitOpts{ContainerDisk: true, Push: "reg.example.com/cua:1"}); err != nil {
		t.Fatalf("runBakePhase5(--container-disk --push): %v", err)
	}
	if gotInImage != deploykit.ContainerDiskPath {
		t.Errorf("in-image path = %q, want the containerDisk contract %q", gotInImage, deploykit.ContainerDiskPath)
	}
	if deploykit.ContainerDiskPath != "/disk/disk.img" {
		t.Fatalf("deploykit.ContainerDiskPath = %q, want /disk/disk.img", deploykit.ContainerDiskPath)
	}
	if pushedDst != "reg.example.com/cua:1" {
		t.Errorf("pushed dst = %q, want the --push ref", pushedDst)
	}

	// Defaults: no --container-disk keeps the VM-box path (""), no --push means
	// no delivery.
	gotInImage, pushedDst = "stale", "stale"
	if err := runBakePhase5("podman", "vm", s, entry, vmBoxEmitOpts{}); err != nil {
		t.Fatalf("runBakePhase5(defaults): %v", err)
	}
	if gotInImage != "" {
		t.Errorf("default in-image path = %q, want \"\" (the VM-box /disk.qcow2 default)", gotInImage)
	}
	if pushedDst != "stale" {
		t.Errorf("no --push must not deliver, got dst %q", pushedDst)
	}

	// The emit is load-bearing (a bake that cannot emit its box has not produced
	// its artifact); a push failure under --push is returned too.
	bakeEmitBox = func(string, string, *VmSpec, string, string) (string, error) {
		return "", fmt.Errorf("boom")
	}
	if err := runBakePhase5("podman", "vm", s, entry, vmBoxEmitOpts{Push: "reg.example.com/x:1"}); err == nil {
		t.Error("an emit failure must be returned, not warned")
	}
	if err := runBakePhase5("podman", "vm", s, entry, vmBoxEmitOpts{}); err == nil {
		t.Error("an emit failure must be returned (the bake's emit is load-bearing)")
	}
	bakeEmitBox = func(string, string, *VmSpec, string, string) (string, error) { return "localhost/x:1", nil }
	bakePushBox = func(string, string, string) error { return fmt.Errorf("push boom") }
	if err := runBakePhase5("podman", "vm", s, entry, vmBoxEmitOpts{Push: "reg.example.com/x:1"}); err == nil {
		t.Error("a push failure under --push must be returned")
	}
}

// TestRunBakePhase5_Live runs the CHANGED runtime path live: phase 5 emits the
// frozen disk as a containerDisk box and pushes it to a REAL registry named by
// CHARLY_TEST_REGISTRY, then re-pulls it and asserts the delivered layer is the
// +gzip form. LIVE-OR-SKIP: unset env → a clean, visible skip (the registry
// boundary is never mocked). The bake's earlier phases (materialize/boot/freeze)
// are unchanged and are proven by the existing bake path + the consumer bed.
func TestRunBakePhase5_Live(t *testing.T) {
	reg := os.Getenv("CHARLY_TEST_REGISTRY")
	if reg == "" {
		t.Skip("CHARLY_TEST_REGISTRY unset — skipping the live phase-5 delivery (LIVE-OR-SKIP; set it to a real registry, e.g. localhost:5099)")
	}
	for _, bin := range []string{"podman", "skopeo"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available on this host — skipping the live phase-5 delivery: %v", bin, err)
		}
	}

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte{0x01}, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}
	entry := &SnapshotEntry{Name: "baked", DiskPath: diskPath}
	dstRef := reg + "/charly-bake-containerdisk-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", dstRef).Run() })

	if err := runBakePhase5("podman", "vm-bake-containerdisk-test", emitFixtureSpec(), entry,
		vmBoxEmitOpts{ContainerDisk: true, Push: dstRef}); err != nil {
		t.Fatalf("runBakePhase5(--container-disk --push): %v", err)
	}

	// The delivered ref must be registry-consumable AND carry the +gzip
	// containerDisk layer.
	_ = exec.Command("podman", "rmi", "-f", dstRef).Run()
	if out, err := exec.Command("podman", "pull", dstRef).CombinedOutput(); err != nil {
		t.Fatalf("pulling the pushed ref %s back failed: %v\n%s", dstRef, err, out)
	}
	raw, err := exec.Command("skopeo", "inspect", "--raw", "docker://"+dstRef).Output()
	if err != nil {
		t.Fatalf("skopeo inspect --raw %s: %v", dstRef, err)
	}
	mt, err := pushedLayerMediaType(raw)
	if err != nil {
		t.Fatalf("reading the pushed layer media type: %v", err)
	}
	if mt != "application/vnd.oci.image.layer.v1.tar+gzip" {
		t.Fatalf("pushed layer media type = %q, want application/vnd.oci.image.layer.v1.tar+gzip", mt)
	}
}
