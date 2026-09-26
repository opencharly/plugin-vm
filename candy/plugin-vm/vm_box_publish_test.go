package vm

// vm_box_publish_test.go — the pure contract of `charly vm box publish`: the
// container-disk-emit envelope it hands verb:oci (the +gzip/`/disk/disk.img`
// contract + the spec.VmBoxMetadata labels) and the result guard. Both halves
// are extracted from the reverse-channel call so they are testable with no host
// executor. The end-to-end produce path is exercised live by the
// check-cua-fleet-build-vm bed.

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// TestContainerDiskEmitEnvelope pins the wire envelope: the disk and ref ride
// through, the in-image path defaults to the KubeVirt/Cua contract, the
// oci_op selector is container-disk-emit, and the metadata labels carry the
// SAME spec.VmBoxMetadata JSON the local VM box does.
func TestContainerDiskEmitEnvelope(t *testing.T) {
	s := emitFixtureSpec()
	params, env, err := containerDiskEmitEnvelope("clone-vm", s, "/var/lib/charly/disk.qcow2", "reg.example.com/cua:1", "", "", false)
	if err != nil {
		t.Fatalf("containerDiskEmitEnvelope: %v", err)
	}

	var envMap map[string]string
	if err := json.Unmarshal(env, &envMap); err != nil {
		t.Fatalf("decode env: %v", err)
	}
	if envMap["oci_op"] != "container-disk-emit" {
		t.Fatalf("oci_op = %q, want container-disk-emit", envMap["oci_op"])
	}

	var req containerDiskEmitRequest
	if err := json.Unmarshal(params, &req); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if req.DiskPath != "/var/lib/charly/disk.qcow2" {
		t.Errorf("disk_path = %q, want the disk argument", req.DiskPath)
	}
	if req.Ref != "reg.example.com/cua:1" {
		t.Errorf("ref = %q, want the registry ref", req.Ref)
	}
	if req.InImagePath != deploykit.ContainerDiskPath {
		t.Errorf("in_image_path = %q, want the default %q", req.InImagePath, deploykit.ContainerDiskPath)
	}
	if deploykit.ContainerDiskPath != "/disk/disk.img" {
		t.Fatalf("deploykit.ContainerDiskPath = %q, want the containerDisk contract /disk/disk.img", deploykit.ContainerDiskPath)
	}

	// The label must be the metadata contract, readable back field-for-field.
	raw := req.Labels[spec.LabelVmBox]
	if raw == "" {
		t.Fatalf("labels carry no %s (got %v)", spec.LabelVmBox, req.Labels)
	}
	var meta spec.VmBoxMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("decode %s: %v", spec.LabelVmBox, err)
	}
	if meta.Source.Kind != "clone" || meta.Source.FromVm != "base-vm" {
		t.Errorf("metadata provenance = %+v, want the resolved clone source", meta.Source)
	}
	if meta.Distro != "fedora" {
		t.Errorf("metadata distro = %q, want fedora", meta.Distro)
	}
}

// TestContainerDiskEmitExplicitInImagePath pins that an explicit in-image path
// overrides the default (the non-default KubeVirt VMI path case).
func TestContainerDiskEmitExplicitInImagePath(t *testing.T) {
	params, _, err := containerDiskEmitEnvelope("vm", emitFixtureSpec(), "/d.img", "reg/x:1", "/custom/disk.qcow2", "/tmp/layout", true)
	if err != nil {
		t.Fatal(err)
	}
	var req containerDiskEmitRequest
	if err := json.Unmarshal(params, &req); err != nil {
		t.Fatal(err)
	}
	if req.InImagePath != "/custom/disk.qcow2" {
		t.Errorf("in_image_path = %q, want the explicit path", req.InImagePath)
	}
	if req.LayoutDir != "/tmp/layout" || !req.Insecure {
		t.Errorf("layout_dir/insecure not threaded: %+v", req)
	}
}

// TestValidateContainerDiskReply is the failure-mode guard: a missing digest or
// a non-+gzip layer must be a real error, never a green publish.
func TestValidateContainerDiskReply(t *testing.T) {
	const gzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	if err := validateContainerDiskReply(containerDiskEmitReply{Digest: "sha256:abc", MediaType: gzip}); err != nil {
		t.Fatalf("a +gzip reply with a digest must pass: %v", err)
	}
	if err := validateContainerDiskReply(containerDiskEmitReply{MediaType: gzip}); err == nil {
		t.Error("a reply with no digest must fail")
	}
	if err := validateContainerDiskReply(containerDiskEmitReply{Digest: "sha256:abc", MediaType: "application/vnd.oci.image.layer.v1.tar"}); err == nil {
		t.Error("an uncompressed-tar reply must fail (the +gzip contract)")
	}
}

// TestPublishContainerDiskNoReverseChannel proves the command fails loudly when
// it has no host reverse channel (an out-of-process placement), rather than
// pretending the artifact was published.
func TestPublishContainerDiskNoReverseChannel(t *testing.T) {
	saved := cmdExec
	cmdExec = nil
	defer func() { cmdExec = saved }()
	if _, err := publishContainerDisk("vm", emitFixtureSpec(), "/d.img", "reg/x:1", "", "", false); err == nil {
		t.Fatal("publishContainerDisk with no reverse channel must error")
	}
}
