package vm

// vm_box_emit_test.go — the box-emission step of `charly vm build` (cutover
// plan task 3). Two pure contract tests over the metadata mapping + label wire
// round-trip (the plugin-side mirror of the spec/sdk VM-box tests) and one live
// integration test that EMITS a VM box image (emitVmBox → deploykit.EmitVmBox:
// scratch image + disk layer + metadata labels), reads it back
// (deploykit.VmCapabilitiesFromLabels), and asserts equality — proving the
// plugin's box-emission step produces a metadata-carrying, from-box-readable
// artifact on local podman storage. The integration test skips when podman is
// unavailable (t.Skip).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// emitFixtureSpec builds a populated clone VmSpec (the flat union carries every
// arm's fields, so the mapping test can pin each field's source). Firmware /
// ssh / cloud_init are entity-level, so they map for every source kind.
func emitFixtureSpec() *VmSpec {
	s := &VmSpec{}
	s.Source.Kind = "clone"
	s.Source.FromVm = "base-vm"
	s.Source.FromSnapshot = "golden"
	s.Source.Distro = "fedora"
	s.Source.BaseUser = "fedora"
	s.Firmware = "uefi-secure"
	s.SSH = &spec.VmSsh{User: "charly"}
	s.CloudInit = &spec.VmCloudInit{CharlyInstall: &spec.VmCharlyInstall{Strategy: "auto"}}
	return s
}

// TestBuildVmBoxMetadata pins the VmSpec → VmBoxMetadata field mapping of the
// box-emission step: distro/base_user ride the source, firmware/ssh/charly_install
// ride the entity, source provenance carries kind + from_vm/from_snapshot, and
// version/description/arch are derived (CalVer-shaped, host arch, box name).
func TestBuildVmBoxMetadata(t *testing.T) {
	s := emitFixtureSpec()
	meta := buildVmBoxMetadata("clone-vm", s)

	if meta.Distro != s.Source.Distro {
		t.Errorf("Distro: got %q, want the source distro %q", meta.Distro, s.Source.Distro)
	}
	if meta.BaseUser != s.Source.BaseUser {
		t.Errorf("BaseUser: got %q, want the source base_user %q", meta.BaseUser, s.Source.BaseUser)
	}
	if meta.Firmware != s.Firmware {
		t.Errorf("Firmware: got %q, want the entity firmware %q", meta.Firmware, s.Firmware)
	}
	if meta.SSHUser != s.SSH.User {
		t.Errorf("SSHUser: got %q, want the entity ssh user %q", meta.SSHUser, s.SSH.User)
	}
	if meta.CharlyInstall != s.CloudInit.CharlyInstall.Strategy {
		t.Errorf("CharlyInstall: got %q, want the entity strategy %q", meta.CharlyInstall, s.CloudInit.CharlyInstall.Strategy)
	}
	if meta.Source.Kind != "clone" {
		t.Errorf("Source.Kind: got %q, want %q", meta.Source.Kind, s.Source.Kind)
	}
	if meta.Source.FromVm != "base-vm" || meta.Source.FromSnapshot != "golden" {
		t.Errorf("Source provenance: got from_vm=%q from_snapshot=%q, want base-vm@golden", meta.Source.FromVm, meta.Source.FromSnapshot)
	}
	if meta.Source.Box != "" || meta.Source.URL != "" {
		t.Errorf("Source provenance: a clone source must not carry box/url; got box=%q url=%q", meta.Source.Box, meta.Source.URL)
	}
	if meta.Arch != runtime.GOARCH {
		t.Errorf("Arch: got %q, want the host arch %q", meta.Arch, runtime.GOARCH)
	}
	if meta.Description != "charly VM box for clone-vm" {
		t.Errorf("Description: got %q, want the box-name description", meta.Description)
	}
	// Version is the current CalVer (YYYY.DDD.HHMM — the repo convention) and is
	// what the image tag rides on.
	calver := regexp.MustCompile(`^\d{4}\.\d{3}\.\d{4}$`)
	if !calver.MatchString(meta.Version) {
		t.Errorf("Version: got %q, want a YYYY.DDD.HHMM CalVer", meta.Version)
	}

	// Arms without an adopted account or install strategy leave the fields empty
	// rather than guessing (best-effort contract).
	bare := &VmSpec{}
	bare.Source.Kind = "clone"
	bare.Source.FromVm = "base-vm"
	bare.Source.FromSnapshot = "golden"
	bareMeta := buildVmBoxMetadata("bare-clone", bare)
	if bareMeta.BaseUser != "" || bareMeta.SSHUser != "" || bareMeta.CharlyInstall != "" || bareMeta.Distro != "" {
		t.Errorf("a bare clone spec must emit empty distro/base_user/ssh_user/charly_install (not resolvable from the spec); got %+v", bareMeta)
	}
}

// TestVmBoxMetadataLabelRoundTrip proves the whole VmBoxMetadata struct the
// emission step writes round-trips through its single JSON OCI label
// (ai.opencharly.vm.box): marshal → unmarshal is an identity. EmitVmBox writes
// exactly this JSON as the label value and VmCapabilitiesFromLabels reads the
// struct back from it (plugin-side mirror of the spec + sdk round-trip tests).
func TestVmBoxMetadataLabelRoundTrip(t *testing.T) {
	in := spec.VmBoxMetadata{
		Distro:        "fedora",
		Arch:          "x86_64",
		BaseUser:      "fedora",
		SSHUser:       "charly",
		Firmware:      "uefi-secure",
		Init:          "systemd",
		CharlyInstall: "scp",
		Version:       "2026.246.0545",
		Source: spec.VmBoxSource{
			Kind:         "clone",
			FromVm:       "base-vm",
			FromSnapshot: "snap-1",
		},
		Description: "charly VM box for clone-vm",
	}

	wire, err := json.Marshal(&in)
	if err != nil {
		t.Fatalf("marshal VmBoxMetadata: %v", err)
	}

	var out spec.VmBoxMetadata
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatalf("unmarshal VmBoxMetadata: %v\nwire: %s", err, wire)
	}

	if !reflect.DeepEqual(in, out) {
		t.Errorf("VmBoxMetadata did not round-trip through JSON:\n in: %+v\nout: %+v", in, out)
	}
}

// TestEmitVmBoxReadBackRoundTrip is the live integration test of the emission
// step: emitVmBox a tiny fixture "disk" (a 1-byte file) with a populated entity
// into local podman storage, read the metadata contract back with
// deploykit.VmCapabilitiesFromLabels, and assert equality with the metadata
// buildVmBoxMetadata derives. Skips when podman is not available on the host.
func TestEmitVmBoxReadBackRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping VM box emit/read-back integration test: %v", err)
	}

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte{0x01}, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}

	s := emitFixtureSpec()
	vmName := "vm-box-emit-test"
	want := buildVmBoxMetadata(vmName, s)

	ref, err := emitVmBox("podman", vmName, s, diskPath, "")
	if err != nil {
		t.Fatalf("emitVmBox: %v", err)
	}
	if wantRef := "localhost/charly-" + vmName + ":" + want.Version; ref != wantRef {
		t.Fatalf("emitVmBox returned ref %q, want %q", ref, wantRef)
	}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rmi", "-f", ref).Run()
	})

	got, err := deploykit.VmCapabilitiesFromLabels("podman", ref)
	if err != nil {
		t.Fatalf("VmCapabilitiesFromLabels: %v", err)
	}
	if got == nil {
		t.Fatal("VmCapabilitiesFromLabels returned nil metadata")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VM box metadata did not round-trip through the emitted image:\n in: %+v\nout: %+v", want, got)
	}
}

// TestBoxInImagePath pins the --container-disk → in-image path mapping: set emits
// the KubeVirt containerDisk contract (/disk/disk.img); unset keeps the emitter's
// default VM-box layout (empty path → /disk.qcow2).
func TestBoxInImagePath(t *testing.T) {
	if got := boxInImagePath(true); got != deploykit.ContainerDiskPath {
		t.Errorf("boxInImagePath(true) = %q, want the containerDisk contract %q", got, deploykit.ContainerDiskPath)
	}
	if got := boxInImagePath(false); got != "" {
		t.Errorf("boxInImagePath(false) = %q, want the default (\"\")", got)
	}
}

// TestPushVmBox_Argv pins the push argv: a srcRef different from dstRef retags
// then pushes; an equal (or empty) srcRef skips the tag and pushes only; an empty
// destination is rejected before any engine call. The engine is stubbed, so no
// registry or live engine is needed.
func TestPushVmBox_Argv(t *testing.T) {
	orig := engineCmd
	t.Cleanup(func() { engineCmd = orig })

	var calls [][]string
	engineCmd = func(binary string, args ...string) error {
		calls = append(calls, append([]string{binary}, args...))
		return nil
	}

	wantPush := []string{"podman", "push", "reg.example/charly-vm:1"}
	wantTag := []string{"podman", "tag", "localhost/charly-vm:1", "reg.example/charly-vm:1"}
	wantStableTag := []string{"podman", "tag", "reg.example/charly-vm:1", "reg.example/charly-vm:latest"}
	wantStablePush := []string{"podman", "push", "reg.example/charly-vm:latest"}

	// src != dst: tag + push, THEN the stable `:latest` twin at the destination
	// (tag + push) — so a consumer that names the box statically can pull it.
	if err := pushVmBox("podman", "localhost/charly-vm:1", "reg.example/charly-vm:1"); err != nil {
		t.Fatalf("pushVmBox: %v", err)
	}
	if want := [][]string{wantTag, wantPush, wantStableTag, wantStablePush}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("pushVmBox engine calls = %v, want %v", calls, want)
	}

	// Same ref: the src tag is skipped; the dst push + the stable twin remain.
	calls = nil
	if err := pushVmBox("podman", "reg.example/charly-vm:1", "reg.example/charly-vm:1"); err != nil {
		t.Fatalf("pushVmBox (same ref): %v", err)
	}
	if want := [][]string{wantPush, wantStableTag, wantStablePush}; !reflect.DeepEqual(calls, want) {
		t.Errorf("same-ref push calls = %v, want %v", calls, want)
	}

	// Empty destination is rejected before any engine call.
	calls = nil
	if err := pushVmBox("podman", "src", ""); err == nil {
		t.Error("pushVmBox with an empty destination should error")
	}
	if len(calls) != 0 {
		t.Errorf("empty-destination push made engine calls: %v", calls)
	}
}

// TestPushVmBox_Live is the LIVE delivery proof of `--push`: it emits a
// containerDisk-layout box, pushes it to a REAL registry named by
// CHARLY_TEST_REGISTRY (e.g. localhost:5000), and re-pulls it to prove the ref is
// registry-consumable. LIVE-OR-SKIP by contract: with CHARLY_TEST_REGISTRY unset
// the test SKIPS cleanly (visibly) — it never mocks the registry boundary.
func TestPushVmBox_Live(t *testing.T) {
	reg := os.Getenv("CHARLY_TEST_REGISTRY")
	if reg == "" {
		t.Skip("CHARLY_TEST_REGISTRY unset — skipping the live registry push (LIVE-OR-SKIP; set it to a real registry, e.g. localhost:5000)")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the live registry push: %v", err)
	}

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte{0x01}, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}

	s := emitFixtureSpec()
	srcRef, err := emitVmBox("podman", "vm-box-push-test", s, diskPath, boxInImagePath(true))
	if err != nil {
		t.Fatalf("emitVmBox: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", srcRef).Run() })

	dstRef := reg + "/charly-vm-box-push-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", dstRef).Run() })

	if err := pushVmBox("podman", srcRef, dstRef); err != nil {
		t.Fatalf("pushVmBox to %s: %v", dstRef, err)
	}

	// Prove the ref is really registry-consumable: drop the local copy, pull it
	// back by the pushed ref.
	_ = exec.Command("podman", "rmi", "-f", dstRef).Run()
	if out, err := exec.Command("podman", "pull", dstRef).CombinedOutput(); err != nil {
		t.Fatalf("pulling the pushed ref %s back failed: %v\n%s", dstRef, err, out)
	}

	// Prove the PUSHED artifact is the containerDisk contract: an OCI manifest
	// with a single application/vnd.oci.image.layer.v1.tar+gzip layer. The push
	// compresses buildah's uncompressed local layer on the wire; this locks that
	// the delivered media type is the +gzip form Cua Fleet / KubeVirt require.
	if _, err := exec.LookPath("skopeo"); err != nil {
		t.Skipf("skopeo not available — cannot verify the pushed media type: %v", err)
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
		t.Fatalf("pushed layer media type = %q, want application/vnd.oci.image.layer.v1.tar+gzip (the containerDisk contract)", mt)
	}
}

// pushedLayerMediaType parses the single disk layer's media type out of a pushed
// OCI manifest — the pure half of the live contract assertions.
func pushedLayerMediaType(rawManifest []byte) (string, error) {
	var m struct {
		Layers []struct {
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		return "", err
	}
	if len(m.Layers) != 1 {
		return "", fmt.Errorf("pushed manifest has %d layers (a containerDisk has exactly one)", len(m.Layers))
	}
	return m.Layers[0].MediaType, nil
}

// TestVmBuildCmd_FlagWiring drives the COMMAND, not the helpers: with the drive
// seam stubbed, VmBuildCmd.Run must pass the parsed flag values through to
// runVmBuildDrive's opts. This is the coverage that FAILS without the
// --container-disk/--push wiring (the helpers alone would still pass).
func TestVmBuildCmd_FlagWiring(t *testing.T) {
	orig := vmBuildDrive
	t.Cleanup(func() { vmBuildDrive = orig })

	var gotOpts vmBoxEmitOpts
	var gotReq spec.VmBuildRequest
	vmBuildDrive = func(box string, req spec.VmBuildRequest, emit vmBoxEmitOpts) error {
		gotOpts = emit
		gotReq = req
		return nil
	}

	cmd := &VmBuildCmd{Box: "myvm", Type: "qcow2", ContainerDisk: true, Push: "reg.example/box:1"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("VmBuildCmd.Run: %v", err)
	}
	if !gotOpts.ContainerDisk || gotOpts.Push != "reg.example/box:1" {
		t.Errorf("flag wiring: Run passed opts %+v, want {ContainerDisk:true Push:reg.example/box:1}", gotOpts)
	}
	if gotReq.Box != "myvm" {
		t.Errorf("request Box = %q, want myvm", gotReq.Box)
	}

	// Defaults: unset flags → the zero-value opts.
	gotOpts = vmBoxEmitOpts{ContainerDisk: true, Push: "stale"}
	cmd = &VmBuildCmd{Box: "myvm", Type: "qcow2"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("VmBuildCmd.Run (defaults): %v", err)
	}
	if gotOpts.ContainerDisk || gotOpts.Push != "" {
		t.Errorf("default flag wiring: opts = %+v, want the zero value", gotOpts)
	}
}

// TestEmitVmBox_ContainerDiskLayoutLive proves the generated box is a valid
// KubeVirt containerDisk payload at its real boundary: emit with the
// containerDisk path, then read the image rootfs with `podman create` +
// `podman cp` and assert the disk is at /disk/disk.img and NOT at the VM-box
// default /disk.qcow2 (KubeVirt scans /disk and boots the single file it finds).
// The metadata label is read back from the SAME image. Skips without podman.
func TestEmitVmBox_ContainerDiskLayoutLive(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the containerDisk layout proof: %v", err)
	}

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte("qcow2-fixture-payload"), 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}

	s := emitFixtureSpec()
	srcRef, err := emitVmBox("podman", "vm-box-cd-layout", s, diskPath, boxInImagePath(true))
	if err != nil {
		t.Fatalf("emitVmBox(containerDisk): %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", srcRef).Run() })

	// The metadata contract must ride the same image (R8: emitted artifact).
	if _, err := deploykit.VmCapabilitiesFromLabels("podman", srcRef); err != nil {
		t.Fatalf("VmCapabilitiesFromLabels on the containerDisk box: %v", err)
	}

	// Read the rootfs with a created (not running) container: `podman cp` reads a
	// scratch image's files without needing a shell inside it.
	cidOut, err := exec.Command("podman", "create", srcRef, "/bin/true").Output()
	if err != nil {
		t.Fatalf("podman create: %v", err)
	}
	cid := strings.TrimSpace(string(cidOut))
	t.Cleanup(func() { _ = exec.Command("podman", "rm", "-f", cid).Run() })

	got := filepath.Join(t.TempDir(), "disk.img")
	if out, err := exec.Command("podman", "cp", cid+":/disk/disk.img", got).CombinedOutput(); err != nil {
		t.Fatalf("the containerDisk payload has no /disk/disk.img: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(got); err != nil || string(b) != "qcow2-fixture-payload" {
		t.Errorf("extracted /disk/disk.img = %q (err=%v), want the fixture payload", b, err)
	}
	if out, err := exec.Command("podman", "cp", cid+":/disk.qcow2", filepath.Join(t.TempDir(), "x")).CombinedOutput(); err == nil {
		t.Errorf("the containerDisk payload must NOT carry the VM-box default /disk.qcow2; cp succeeded:\n%s", out)
	}
}

// TestEmitVmBox_StableTagLive: the emit writes a stable :latest handle alongside the
// wall-clock CalVer tag — a consumer that must NAME the box (a kind:kubevirt
// containerDisk.image) cannot know the CalVer. Fails without the stable tag.
func TestEmitVmBox_StableTagLive(t *testing.T) {
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping: %v", err)
	}
	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(diskPath, []byte{0x01}, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}
	s := emitFixtureSpec()
	ref, err := emitVmBox("podman", "vm-box-stable-tag", s, diskPath, "")
	if err != nil {
		t.Fatalf("emitVmBox: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("podman", "rmi", "-f", ref, "localhost/charly-vm-box-stable-tag:latest").Run()
	})
	if err := exec.Command("podman", "image", "exists", "localhost/charly-vm-box-stable-tag:latest").Run(); err != nil {
		t.Fatalf("no stable :latest handle after emit: %v", err)
	}
}

// TestLatestRef pins the destination-side stable-handle computation: the `--push`
// path publishes BOTH the CalVer ref and a `:latest` handle at the registry, so a
// consumer that must NAME the box statically (a kind:kubevirt containerDisk.image)
// resolves it. A registry host:port is preserved; a digest-pinned ref is unchanged.
func TestLatestRef(t *testing.T) {
	cases := []struct{ in, want string }{
		{"registry.example.com/charly-box:2026.270.0000", "registry.example.com/charly-box:latest"},
		{"registry.example.com:5000/charly-box:v1", "registry.example.com:5000/charly-box:latest"},
		{"registry.example.com/charly-box", "registry.example.com/charly-box:latest"},
		{"localhost/charly-vm:latest", "localhost/charly-vm:latest"},
		{"registry.example.com/charly-box@sha256:abc", "registry.example.com/charly-box@sha256:abc"},
	}
	for _, c := range cases {
		if got := latestRef(c.in); got != c.want {
			t.Errorf("latestRef(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
