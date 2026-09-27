package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestContainerDiskImportSource gates the synthesis: `charly vm import-container-disk`
// builds the SAME #VmSource a `container_disk` entity carries, so the cache key and the
// pull engine are shared with the build path (R3). Fails without the synthesis (e.g. a
// missing DiskPathInImage/Cache field would silently key the cache differently).
func TestContainerDiskImportSource(t *testing.T) {
	src := containerDiskImportSource("public.ecr.aws/k5j5w0x5/cua@sha256:abc", "/disk/disk.img", "/tmp/cache")
	if src.Kind != "container_disk" {
		t.Errorf("Kind = %q, want container_disk", src.Kind)
	}
	if src.Image != "public.ecr.aws/k5j5w0x5/cua@sha256:abc" {
		t.Errorf("Image = %q, want the passed ref", src.Image)
	}
	if src.DiskPathInImage != "/disk/disk.img" {
		t.Errorf("DiskPathInImage = %q, want /disk/disk.img", src.DiskPathInImage)
	}
	if src.Cache != "/tmp/cache" {
		t.Errorf("Cache = %q, want /tmp/cache", src.Cache)
	}
}

// TestContainerDiskCacheDir_DigestKeyed proves the import and the build share the ONE
// content-addressed cache: a digest-pinned ref keys the directory by the digest (so two
// refs naming the same manifest share the cache), and the cache root is honoured.
func TestContainerDiskCacheDir_DigestKeyed(t *testing.T) {
	root := t.TempDir()
	a, err := containerDiskCacheDir(containerDiskImportSource("reg/x@sha256:deadbeef", "", root))
	if err != nil {
		t.Fatalf("containerDiskCacheDir: %v", err)
	}
	if filepath.Dir(a) != root {
		t.Errorf("cache dir %q is not under the override root %q", a, root)
	}
	if filepath.Base(a) != "sha256-deadbeef" {
		t.Errorf("cache dir base = %q, want the digest-keyed sha256-deadbeef", filepath.Base(a))
	}
}

// TestImportContainerDisk_Live drives the REAL import against a real registry: emit a
// containerDisk fixture, push it, then import it into a temp cache and assert the disk
// bytes and the identity marker landed. LIVE-OR-SKIP by contract: with
// CHARLY_TEST_REGISTRY unset it SKIPS visibly (never mocks the registry boundary).
func TestImportContainerDisk_Live(t *testing.T) {
	reg := os.Getenv("CHARLY_TEST_REGISTRY")
	if reg == "" {
		t.Skip("CHARLY_TEST_REGISTRY unset — skipping the live import (LIVE-OR-SKIP; set it to a real registry, e.g. localhost:5000)")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skipf("podman not available on this host — skipping the live import: %v", err)
	}
	if _, err := exec.LookPath("skopeo"); err != nil {
		t.Skipf("skopeo not available on this host — skipping the live import: %v", err)
	}

	diskPath := filepath.Join(t.TempDir(), "disk.qcow2")
	want := []byte{0x42}
	if err := os.WriteFile(diskPath, want, 0o644); err != nil {
		t.Fatalf("writing fixture disk: %v", err)
	}
	srcRef, err := emitVmBox("podman", "vm-import-container-disk-test", emitFixtureSpec(), diskPath, boxInImagePath(true))
	if err != nil {
		t.Fatalf("emitVmBox: %v", err)
	}
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", srcRef).Run() })

	dstRef := reg + "/charly-vm-import-container-disk-test:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = exec.Command("podman", "rmi", "-f", dstRef).Run() })
	if err := pushVmBox("podman", srcRef, dstRef); err != nil {
		t.Fatalf("pushVmBox to %s: %v", dstRef, err)
	}

	cache := t.TempDir()
	cmd := &VmImportContainerDiskCmd{Image: dstRef, Cache: cache}
	if err := cmd.Run(); err != nil {
		t.Fatalf("VmImportContainerDiskCmd.Run: %v", err)
	}

	cacheDir, err := containerDiskCacheDir(containerDiskImportSource(dstRef, "", cache))
	if err != nil {
		t.Fatalf("containerDiskCacheDir: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(cacheDir, "disk.qcow2"))
	if err != nil {
		t.Fatalf("reading the imported disk: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("imported disk bytes = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "manifest-digest.txt")); err != nil {
		t.Fatalf("identity marker not written: %v", err)
	}
}

// TestContainerDiskCacheDir_TagVsDigest pins the EXACT keying rule the import shares with
// the build path: a DIGEST-pinned ref keys the cache by the digest (dir `sha256-<hex>`,
// stable across consumers); a TAG ref keys by the sha256 of the ref string. The same
// artifact named by tag and by digest therefore gets two entries — the shared engine's
// intended behaviour, not a silent collapse.
func TestContainerDiskCacheDir_TagVsDigest(t *testing.T) {
	root := t.TempDir()
	tagDir, err := containerDiskCacheDir(containerDiskImportSource("reg/x:1", "", root))
	if err != nil {
		t.Fatalf("containerDiskCacheDir(tag): %v", err)
	}
	digDir, err := containerDiskCacheDir(containerDiskImportSource("reg/x@sha256:deadbeef", "", root))
	if err != nil {
		t.Fatalf("containerDiskCacheDir(digest): %v", err)
	}
	if filepath.Base(digDir) != "sha256-deadbeef" {
		t.Errorf("digest-pinned cache dir base = %q, want sha256-deadbeef", filepath.Base(digDir))
	}
	// The TAG ref is keyed by the sha256 of the REF STRING (exact assertion — not merely
	// "different from the digest dir").
	sum := sha256.Sum256([]byte("reg/x:1"))
	wantTag := hex.EncodeToString(sum[:])
	if filepath.Base(tagDir) != wantTag {
		t.Errorf("tag cache dir base = %q, want the ref hash %q", filepath.Base(tagDir), wantTag)
	}
	if filepath.Base(tagDir) == filepath.Base(digDir) {
		t.Errorf("tag and digest refs share the cache dir %q — keying must differ", tagDir)
	}
	// The digest key is STABLE: the same digest ref always resolves the same dir.
	again, err := containerDiskCacheDir(containerDiskImportSource("reg/x@sha256:deadbeef", "", root))
	if err != nil {
		t.Fatalf("containerDiskCacheDir(digest again): %v", err)
	}
	if again != digDir {
		t.Errorf("digest-pinned cache dir is not stable: %q != %q", again, digDir)
	}
}
