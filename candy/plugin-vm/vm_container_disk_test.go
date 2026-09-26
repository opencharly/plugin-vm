package vm

// vm_container_disk_test.go — the unit gate for the container_disk source arm's pull
// engine. The registry round-trip is not needed to prove the arm's real logic: the
// index→platform-manifest selection, the layer-digest read, the media-type→gzip
// decision, the in-image path default, and the disk extraction from a gzipped/uncompressed
// layer tar are all PURE and are exercised against fixtures here. The live pull is proven
// end to end by the bed (check-cua-container-disk-vm).

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

const indexFixture = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "manifests": [
    {"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:amd64","platform":{"architecture":"amd64","os":"linux"}},
    {"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:attest","platform":{"architecture":"unknown","os":"unknown"}}
  ]
}`

const manifestFixture = `{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "config": {"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:cfg","size":445},
  "layers": [
    {"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:3e3a58","size":3175830663}
  ]
}`

// The ref in the motivating corpus is an INDEX; the engine must select the amd64/linux
// member (the platform the VM runs on), never the attestation manifest.
func TestResolveContainerDiskManifest_IndexOfIndexSelectsAmd64(t *testing.T) {
	digest, mt, err := resolveContainerDiskManifest([]byte(indexFixture))
	if err != nil {
		t.Fatalf("resolve index: %v", err)
	}
	if digest != "sha256:amd64" {
		t.Errorf("selected %q, want the amd64 manifest sha256:amd64", digest)
	}
	if mt != "application/vnd.oci.image.manifest.v1+json" {
		t.Errorf("media type %q", mt)
	}
}

// A plain single-platform manifest carries no `manifests` array; the engine returns it
// unchanged (the caller then reads its layers).
func TestResolveContainerDiskManifest_PlainManifestPassesThrough(t *testing.T) {
	digest, _, err := resolveContainerDiskManifest([]byte(manifestFixture))
	if err != nil {
		t.Fatalf("resolve plain manifest: %v", err)
	}
	if digest != "" {
		t.Errorf("a plain manifest must yield an empty platform digest (nothing to re-resolve), got %q", digest)
	}
}

// A containerDisk is a scratch image whose ONE layer is the disk. The layer digest +
// media type are read from the platform manifest.
func TestContainerDiskLayerDigest(t *testing.T) {
	d, mt, err := containerDiskLayerDigest([]byte(manifestFixture))
	if err != nil {
		t.Fatalf("layer digest: %v", err)
	}
	if d != "sha256:3e3a58" {
		t.Errorf("layer digest %q, want sha256:3e3a58", d)
	}
	if !isGzipLayer(mt) {
		t.Errorf("media type %q must be recognised as gzip", mt)
	}
}

// The media type — not a byte sniff — decides gzip. Both real forms are accepted:
// Cua's +gzip and a buildah-emitted uncompressed tar.
func TestIsGzipLayer(t *testing.T) {
	if !isGzipLayer("application/vnd.oci.image.layer.v1.tar+gzip") {
		t.Error("+gzip must be gzip")
	}
	if isGzipLayer("application/vnd.oci.image.layer.v1.tar") {
		t.Error("a plain tar must NOT be treated as gzip")
	}
}

// The default in-image path is the KubeVirt contract; an authored path wins.
func TestContainerDiskInImagePath(t *testing.T) {
	if got := containerDiskInImagePath(VmSource{Kind: "container_disk"}); got != "/disk/disk.img" {
		t.Errorf("default path %q, want /disk/disk.img", got)
	}
	if got := containerDiskInImagePath(VmSource{Kind: "container_disk", DiskPathInImage: "/custom/disk.qcow2"}); got != "/custom/disk.qcow2" {
		t.Errorf("authored path %q", got)
	}
}

// buildLayer writes a tar (optionally gzipped) holding the given entries.
func buildLayer(t *testing.T, gzipped bool, entries map[string][]byte) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	var w *gzip.Writer
	dst := &buf
	if gzipped {
		w = gzip.NewWriter(&buf)
		dst = nil
	}
	_ = dst
	var tw *tar.Writer
	if gzipped {
		tw = tar.NewWriter(w)
	} else {
		tw = tar.NewWriter(&buf)
	}
	for name, body := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if gzipped {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return bytes.NewReader(buf.Bytes())
}

// Extraction must find the disk at the KubeVirt path inside a GZIPPED layer (Cua's real
// form) and inside an UNCOMPRESSED one (buildah's default).
func TestExtractDiskFromLayerTar(t *testing.T) {
	disk := []byte("QFI\xfb fake qcow2 bytes")
	for _, gz := range []bool{true, false} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "disk.qcow2")
		r := buildLayer(t, gz, map[string][]byte{
			"disk/disk.img":           disk,
			"disk/other-not-the-disk": []byte("x"),
		})
		if err := extractDiskFromLayerTar(r, gz, "/disk/disk.img", dest); err != nil {
			t.Fatalf("gzipped=%v extract: %v", gz, err)
		}
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, disk) {
			t.Errorf("gzipped=%v: extracted %q, want %q", gz, got, disk)
		}
	}
}

// A layer without the wanted path must fail NAMING the path — not silently produce no disk.
func TestExtractDiskFromLayerTar_MissingDisk(t *testing.T) {
	r := buildLayer(t, true, map[string][]byte{"disk/something-else": []byte("x")})
	err := extractDiskFromLayerTar(r, true, "/disk/disk.img", filepath.Join(t.TempDir(), "d"))
	if err == nil {
		t.Fatal("expected an error for a layer with no disk")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("/disk/disk.img")) {
		t.Errorf("error must name the wanted path, got: %v", err)
	}
}

// The cache dir is digest-keyed when pinned and ref-hashed otherwise.
func TestContainerDiskCacheDir_KeyedByDigest(t *testing.T) {
	src := VmSource{Kind: "container_disk", Image: "public.ecr.aws/k5j5w0x5/cua-omarchy-workspace@sha256:dd09f70f8cd35eca7871dff000538302baad63688a3008803f2238492802d743"}
	d1, err := containerDiskCacheDir(src)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d1) != "sha256-dd09f70f8cd35eca7871dff000538302baad63688a3008803f2238492802d743" {
		t.Errorf("cache dir key %q must be the digest", filepath.Base(d1))
	}
	// A mutable tag is hashed (stable) rather than used raw.
	src2 := VmSource{Kind: "container_disk", Image: "public.ecr.aws/k5j5w0x5/cua-omarchy-workspace:main"}
	d2, _ := containerDiskCacheDir(src2)
	d3, _ := containerDiskCacheDir(src2)
	if d2 != d3 {
		t.Error("a mutable tag must hash to a stable cache key")
	}
	if filepath.Base(d2) == "main" {
		t.Error("a tag must not be used as a raw path segment")
	}
}
