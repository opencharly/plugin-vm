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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"testing"

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

	ref, err := emitVmBox("podman", vmName, s, diskPath)
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
