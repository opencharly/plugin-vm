package vm

// vm_buildkit_guard_test.go — the live precondition for the VM-box EMIT path, and the control that
// the request context is really threaded (opencharly/opencharly#481 leg 19; both are findings of
// `plugin-vm#84`'s review, carried forward into the PR that supersedes it).
//
// The emit builds through BuildKit now: `deploykit.EmitVmBoxAt` takes a context.Context and drives
// the buildkitdrive kit, so a live emit test needs the drive's TOOLS and not only an engine plus
// qemu-img. Without this guard a host that has podman but not the drive FAILS as though the code
// under test were broken. The sdk ships exactly this guard for its own emit tests
// (sdk/deploykit/buildkitdrive_guard_test.go); it is unexported there, so the SHAPE is reproduced
// here rather than imported — a boundary that crosses a real service SKIPS VISIBLY when its
// requirement is absent, is never mocked, and never reports a missing tool as a code failure.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireBuildkitDrive skips VISIBLY when the BuildKit toolchain is absent, naming the tool.
func requireBuildkitDrive(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"rootlesskit", "buildkitd", "buildctl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed — skipping the live BuildKit emit path: %v", tool, err)
		}
	}
}

// TestEmitVmBoxHonorsTheRequestContext is the control that fails if the real request context is NOT
// threaded: a CANCELLED context must abort the emit. Threading `context.Background()` at the call
// site (or dropping the ctx from the drive call, which is what leg 3's signature change made
// possible to do silently) makes this test see a SUCCESSFUL emit instead, so the arm is what keeps
// "the engine build is cancellable with the request that asked for it" from being a claim about a
// signature rather than about behaviour.
func TestEmitVmBoxHonorsTheRequestContext(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the live emit cancellation arm: %v", err)
	}
	requireBuildkitDrive(t)

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte{0x01}, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled BEFORE the emit: nothing about this request may proceed

	ref, err := emitVmBox(ctx, "podman", "vm-box-cancel-test", emitFixtureSpec(), diskPath, "")
	if err == nil {
		t.Fatalf("a CANCELLED context emitted successfully (ref %q) — the request context is not threaded to the drive", ref)
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(strings.ToLower(err.Error()), "context canceled") {
		t.Fatalf("the emit aborted, but not because of the cancelled context — the arm would pass for the wrong reason: %v", err)
	}
}
