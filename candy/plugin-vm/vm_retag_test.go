package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// TestRetagImage_Argv pins the retag argv and its edge cases: a distinct dst
// tags; a same-ref retag is a no-op; an empty dst is rejected. The engine is
// stubbed, so no live engine is needed.
func TestRetagImage_Argv(t *testing.T) {
	orig := engineCmd
	t.Cleanup(func() { engineCmd = orig })

	var calls [][]string
	engineCmd = func(binary string, args ...string) error {
		calls = append(calls, append([]string{binary}, args...))
		return nil
	}

	if err := retagImage("podman", "localhost/charly-vm:2026.269.1", "localhost/charly-vm:stable"); err != nil {
		t.Fatalf("retagImage: %v", err)
	}
	want := []string{"podman", "tag", "localhost/charly-vm:2026.269.1", "localhost/charly-vm:stable"}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0], want) {
		t.Errorf("retag argv = %v, want [%v]", calls, want)
	}

	calls = nil
	if err := retagImage("podman", "same:1", "same:1"); err != nil {
		t.Fatalf("retagImage (same ref): %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("a same-ref retag must be a no-op; got engine calls %v", calls)
	}

	calls = nil
	if err := retagImage("podman", "a:1", ""); err == nil {
		t.Error("retagImage with an empty destination should error")
	}
	if len(calls) != 0 {
		t.Errorf("empty-destination retag made engine calls: %v", calls)
	}
}

// TestVmRetag_Live proves the verb on the real engine: build a tiny scratch
// image, retag it from its CalVer-shaped ref to a stable ref, and confirm the
// stable ref resolves locally. Skips when podman is unavailable.
func TestVmRetag_Live(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the live retag: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), []byte("retag-fixture"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	cf := "FROM scratch\nCOPY disk.qcow2 /disk/disk.img\n"
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatalf("writing Containerfile: %v", err)
	}
	uniq := strconv.FormatInt(time.Now().UnixNano(), 10)
	src := "localhost/vm-retag-live-src:" + uniq
	dst := "localhost/vm-retag-live-dst:" + uniq
	t.Cleanup(func() {
		_ = exec.Command("podman", "rmi", "-f", src, dst).Run()
	})

	if out, err := exec.Command("podman", "build", "-t", src, "-f", filepath.Join(dir, "Containerfile"), dir).CombinedOutput(); err != nil {
		t.Fatalf("podman build: %v\n%s", err, out)
	}

	if err := retagImage("podman", src, dst); err != nil {
		t.Fatalf("retagImage: %v", err)
	}
	if err := exec.Command("podman", "image", "exists", dst).Run(); err != nil {
		t.Fatalf("the destination ref %s does not resolve after retag: %v", dst, err)
	}
}

// buildRetagFixture builds a tiny scratch image and returns its ref + a cleanup.
func buildRetagFixture(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "disk.qcow2"), []byte("retag-fixture"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	cf := "FROM scratch\nCOPY disk.qcow2 /disk/disk.img\n"
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatalf("writing Containerfile: %v", err)
	}
	ref := name + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", ref).Run() })
	if out, err := exec.Command("podman", "build", "-t", ref, "-f", filepath.Join(dir, "Containerfile"), dir).CombinedOutput(); err != nil {
		t.Fatalf("podman build: %v\n%s", err, out)
	}
	return ref
}

// TestVmRetagCmd_RunLive drives the CHANGED RUNNER — `VmRetagCmd.Run()` — on the
// real engine (not just the helper): it builds a scratch image and invokes the
// command to retag it to a stable ref, asserting the destination resolves. With
// CHARLY_TEST_REGISTRY set it also exercises the `--push` arm against a real
// registry (LIVE-OR-SKIP). Skips without podman.
func TestVmRetagCmd_RunLive(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the live VmRetagCmd.Run: %v", err)
	}

	src := buildRetagFixture(t, "localhost/vm-retag-cmd-src")
	dst := src + "-stable"
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", dst).Run() })

	cmd := &VmRetagCmd{Src: src, Dst: dst, Engine: "podman"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("VmRetagCmd.Run: %v", err)
	}
	if err := exec.Command("podman", "image", "exists", dst).Run(); err != nil {
		t.Fatalf("VmRetagCmd.Run did not produce the destination ref %s: %v", dst, err)
	}

	reg := os.Getenv("CHARLY_TEST_REGISTRY")
	if reg == "" {
		t.Log("CHARLY_TEST_REGISTRY unset — skipping the --push arm (LIVE-OR-SKIP)")
		return
	}
	pushed := reg + "/vm-retag-cmd-push:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", pushed).Run() })
	pushCmd := &VmRetagCmd{Src: src, Dst: pushed, Push: true, Engine: "podman"}
	if err := pushCmd.Run(); err != nil {
		t.Fatalf("VmRetagCmd.Run --push: %v", err)
	}
	_ = exec.Command("podman", "rmi", "-f", pushed).Run()
	if out, err := exec.Command("podman", "pull", pushed).CombinedOutput(); err != nil {
		t.Fatalf("pulling the pushed ref %s back failed: %v\n%s", pushed, err, out)
	}
}
