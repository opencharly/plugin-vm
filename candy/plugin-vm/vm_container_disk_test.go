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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A manifest with ZERO or MORE THAN ONE layer is NOT a containerDisk (its contract is a
// scratch image whose ONE layer is the disk). Both must be rejected, not silently read as
// layers[0] — the multi-layer case is the one the comment promised but the code did not enforce.
func TestContainerDiskLayerDigest_RejectsNonSingleLayer(t *testing.T) {
	const zero = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`
	const multi = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[` +
		`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:aaa","size":1},` +
		`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:bbb","size":1}]}`
	for name, m := range map[string]string{"zero layers": zero, "multi layer": multi} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := containerDiskLayerDigest([]byte(m)); err == nil {
				t.Errorf("%s: expected a rejection (a containerDisk has exactly one layer)", name)
			}
		})
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
	if gzipped {
		w = gzip.NewWriter(&buf)
	}
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

// TestBuildContainerDiskLive is the LIVE end-to-end proof of the arm's engine: it builds a
// real OCI containerDisk artifact (a qcow2 at /disk/disk.img — the KubeVirt contract),
// pushes it to a REAL registry, and drives BuildContainerDisk against it — the actual
// skopeo inspect → index/manifest resolve → skopeo copy → layer tar extract → qemu-img
// format assert → per-VM copy chain, with the disk landing in the output dir.
//
// Gated on LIVE_CONTAINER_DISK_REGISTRY (the real service): unset → SKIP, visibly. Never a
// mock — a fake registry would assert the behaviour the author imagined, not what the wire
// does. Run it against a local `registry:2`:
//
//	podman run -d -p 5000:5000 --name cua-test-registry docker.io/library/registry:2
//	LIVE_CONTAINER_DISK_REGISTRY=localhost:5000 go test -run TestBuildContainerDiskLive -v
func TestBuildContainerDiskLive(t *testing.T) {
	reg := os.Getenv("LIVE_CONTAINER_DISK_REGISTRY")
	if reg == "" {
		t.Skip("LIVE_CONTAINER_DISK_REGISTRY unset — skipping the live containerDisk pull (no real registry)")
	}
	for _, bin := range []string{"skopeo", "qemu-img", "podman"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available — skipping the live containerDisk pull: %v", bin, err)
		}
	}

	// 1. Build the artifact: a scratch image carrying a REAL qcow2 at /disk/disk.img.
	tmp := t.TempDir()
	disk := filepath.Join(tmp, "disk.img")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", disk, "8M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v\n%s", err, out)
	}
	cf := filepath.Join(tmp, "Containerfile")
	if err := os.WriteFile(cf, []byte("FROM scratch\nCOPY disk.img /disk/disk.img\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := reg + "/cua-test/containerd:" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	if out, err := exec.Command("podman", "build", "-t", ref, "-f", cf, tmp).CombinedOutput(); err != nil {
		t.Fatalf("podman build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", ref).Run() })
	// A localhost test registry serves plain HTTP; mark it insecure via a test-scoped
	// registries.conf (skopeo inherits CONTAINERS_REGISTRIES_CONF), leaving production
	// untouched. The real corpus is an HTTPS registry and needs none of this.
	regConf := filepath.Join(tmp, "registries.conf")
	host, _, _ := strings.Cut(reg, ":")
	if err := os.WriteFile(regConf, []byte("[[registry]]\nlocation = \""+host+"\"\ninsecure = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTAINERS_REGISTRIES_CONF", regConf)
	tlsArgs := []string{}
	if host == "localhost" || host == "127.0.0.1" {
		tlsArgs = append(tlsArgs, "--tls-verify=false")
	}
	// Push the TAG (a digest push would force a layer-representation change) and capture the
	// pushed digest via --digestfile — RepoDigests is unreliable here because identical
	// content can be shared across local repos.
	digestFile := filepath.Join(tmp, "pushed-digest.txt")
	pushArgs := append(append([]string{"push", "--digestfile", digestFile}, tlsArgs...), ref)
	if out, err := exec.Command("podman", pushArgs...).CombinedOutput(); err != nil {
		t.Fatalf("podman push %s: %v\n%s", ref, err, out)
	}
	rawDigest, err := os.ReadFile(digestFile)
	if err != nil {
		t.Fatalf("read pushed digest: %v", err)
	}
	pinned := reg + "/cua-test/containerd@" + strings.TrimSpace(string(rawDigest))
	if !strings.Contains(pinned, "@sha256:") {
		t.Fatalf("no pushed digest: %q", pinned)
	}

	// 2. Drive the engine. Cache dir under the test's temp so the run is hermetic.
	outDir := filepath.Join(tmp, "out")
	stateDir := filepath.Join(tmp, "state")
	for _, d := range []string{outDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vmspec := &VmSpec{
		DiskSize: "",
		Source:   VmSource{Kind: "container_disk", Image: pinned, Cache: filepath.Join(tmp, "cache")},
	}
	res, err := BuildContainerDisk(vmspec, outDir, stateDir, nil, true)
	if err != nil {
		t.Fatalf("BuildContainerDisk(%s): %v", pinned, err)
	}

	// 3. Assert the disk really landed and is a bootable qcow2 (the format the boot path needs).
	if _, err := os.Stat(res.DiskPath); err != nil {
		t.Fatalf("built disk missing at %s: %v", res.DiskPath, err)
	}
	info, err := exec.Command("qemu-img", "info", "--output", "json", res.DiskPath).Output()
	if err != nil {
		t.Fatalf("qemu-img info on the pulled disk: %v", err)
	}
	if !bytes.Contains(info, []byte(`"format": "qcow2"`)) {
		t.Errorf("pulled disk is not qcow2: %s", info)
	}
	// The seed ISO is the cloud_image-equivalent artifact.
	if _, err := os.Stat(res.SeedIsoPath); err != nil {
		t.Errorf("seed ISO missing at %s: %v", res.SeedIsoPath, err)
	}
}

// The cache marker identity must be STABLE across a re-pull of an unchanged artifact (the
// cache-hit contract), and DISTINCT when the artifact changes. The previous code recorded a
// different value than the hit test compared for a plain manifest, so a re-pull never hit.
func TestContainerDiskCacheIdentity_StableAndDistinct(t *testing.T) {
	// An index: the resolved platform digest IS the identity (the ref is irrelevant).
	if got := containerDiskCacheIdentity("sha256:amd64", []byte(`{"any":"bytes"}`)); got != "sha256:amd64" {
		t.Errorf("index identity = %q, want the platform digest sha256:amd64", got)
	}
	// A plain manifest: identity is the raw-bytes hash — deterministic across calls.
	plain := []byte(manifestFixture)
	a := containerDiskCacheIdentity("", plain)
	b := containerDiskCacheIdentity("", plain)
	if a == "" || a != b {
		t.Errorf("plain identity must be a stable non-empty hash: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("plain identity %q must be sha256:<hex>", a)
	}
	// A CHANGED plain manifest yields a DIFFERENT identity (a changed artifact must miss).
	if c := containerDiskCacheIdentity("", append(append([]byte{}, plain...), ' ')); c == a {
		t.Error("a changed manifest must produce a different identity (else a stale disk would be reused)")
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
