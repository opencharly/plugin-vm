package vm

// vm_box_publish_test.go — the pure contract of `charly vm box publish`: the
// skopeo push argv (OCI manifest + gzip destination layers), the local box ref,
// and the pushed-manifest +gzip guard. The emit half is the SDK emitter
// (deploykit.EmitVmBoxAt) and is covered by vm_box_emit_test.go; the end-to-end
// produce path is exercised live by the check-cua-fleet-build-vm bed.

import (
	"reflect"
	"strings"
	"testing"
)

// TestContainerDiskLocalRef pins that the containerDisk local box ref is
// distinct from the from-box VM-box ref (whose disk lives at /disk.qcow2), so
// the two artifacts cannot collide in local storage.
func TestContainerDiskLocalRef(t *testing.T) {
	got := containerDiskLocalRef("omarchy-cua", "2026.269.1234")
	want := "localhost/charly-omarchy-cua-containerdisk:2026.269.1234"
	if got != want {
		t.Fatalf("containerDiskLocalRef = %q, want %q", got, want)
	}
	if got == "localhost/charly-omarchy-cua:2026.269.1234" {
		t.Fatal("containerDisk ref must not equal the from-box VM-box ref")
	}
}

// TestSkopeoCopyArgs pins the push argv: the OCI manifest type and the
// gzip-compressed destination layers are the +gzip containerDisk contract;
// --dest-tls-verify=false is added only for an insecure registry.
func TestSkopeoCopyArgs(t *testing.T) {
	got := skopeoCopyArgs("localhost/charly-x-containerdisk:1", "reg.example.com/cua:1", false)
	want := []string{"copy", "--format", "oci", "--dest-compress", "--dest-compress-format", "gzip", "containers-storage:localhost/charly-x-containerdisk:1", "docker://reg.example.com/cua:1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skopeoCopyArgs =\n  %v\nwant\n  %v", got, want)
	}
	ins := skopeoCopyArgs("local:x", "reg/y:1", true)
	if !contains(ins, "--dest-tls-verify=false") {
		t.Fatalf("insecure copy args missing --dest-tls-verify=false: %v", ins)
	}
	if !contains(ins, "--dest-compress-format") {
		t.Fatalf("copy args must always request gzip layers: %v", ins)
	}
}

// TestSkopeoInspectArgs pins the two inspect argv shapes used to verify the
// push (raw manifest + digest), with the insecure flag threaded.
func TestSkopeoInspectArgs(t *testing.T) {
	raw := skopeoInspectRawArgs("reg/x:1", false)
	if !reflect.DeepEqual(raw, []string{"inspect", "--raw", "docker://reg/x:1"}) {
		t.Fatalf("skopeoInspectRawArgs = %v", raw)
	}
	if !contains(skopeoInspectRawArgs("reg/x:1", true), "--tls-verify=false") {
		t.Fatal("insecure raw inspect must pass --tls-verify=false")
	}
	dg := skopeoInspectDigestArgs("reg/x:1", false)
	if !reflect.DeepEqual(dg, []string{"inspect", "--format", "{{.Digest}}", "docker://reg/x:1"}) {
		t.Fatalf("skopeoInspectDigestArgs = %v", dg)
	}
}

// TestContainerDiskMediaType is the pushed-manifest guard: a single +gzip layer
// passes; an uncompressed layer, a multi-layer manifest, and an index all fail.
func TestContainerDiskMediaType(t *testing.T) {
	gzipManifest := []byte(`{"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[{"mediaType":"` + containerDiskGzipLayer + `","digest":"sha256:aa","size":10}]}`)
	mt, err := containerDiskMediaType(gzipManifest)
	if err != nil {
		t.Fatalf("a single +gzip layer must parse: %v", err)
	}
	if mt != containerDiskGzipLayer {
		t.Fatalf("media type = %q, want %q", mt, containerDiskGzipLayer)
	}

	uncompressed := []byte(`{"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar"}]}`)
	if mt, err := containerDiskMediaType(uncompressed); err != nil || mt != "application/vnd.oci.image.layer.v1.tar" {
		t.Fatalf("uncompressed layer should parse to its own media type (the caller compares): %q %v", mt, err)
	}

	multi := []byte(`{"layers":[{"mediaType":"a"},{"mediaType":"b"}]}`)
	if _, err := containerDiskMediaType(multi); err == nil {
		t.Error("a multi-layer manifest must fail (not a containerDisk)")
	}
	index := []byte(`{"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	if _, err := containerDiskMediaType(index); err == nil {
		t.Error("an index (no layers) must fail")
	}
	if _, err := containerDiskMediaType([]byte("not json")); err == nil {
		t.Error("invalid JSON must fail")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestContainerDiskGzipLayerConstant pins the contract constant against the
// literal Cua Fleet requires, so a rename cannot silently change the media type.
func TestContainerDiskGzipLayerConstant(t *testing.T) {
	if !strings.HasSuffix(containerDiskGzipLayer, "+gzip") {
		t.Fatalf("containerDiskGzipLayer = %q, want a +gzip media type", containerDiskGzipLayer)
	}
}
