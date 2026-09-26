package vm

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// vm_container_disk.go — the `container_disk` source arm's build engine.
//
// A container_disk is an OCI artifact carrying a bootable guest disk (the KubeVirt
// containerDisk contract: the disk lives in a layer, by default at /disk/disk.img, the
// directory KubeVirt scans). The motivating corpus is a Cua Fleet image. This engine
// pulls the artifact, extracts the disk, and hands it to the UNCHANGED cloud-init/SSH
// boot path — so the rest of the VM pipeline (create/ssh/snapshot/clone) needs no change.
//
// Tooling: `skopeo` for the registry fetch (the same host-side dependency plugin-oci's
// merge engine already uses — no new core dependency), `qemu-img` for the format checks,
// and the stdlib tar/gzip for the layer extraction. Everything runs host-side; the plugin
// owns the pull, exactly as the bootc and bootstrap engines own theirs.
//
// The extracted disk is content-addressed under the VM image cache keyed by the manifest
// digest (an index resolves to the platform manifest first), so a re-pull of the same
// pinned artifact is a cache hit and two beds building the same image never race — the
// flock + atomic-rename discipline kit.FetchQcow2 uses.

// containerDiskManifest is the subset of an OCI image manifest this engine reads: the
// config and the ordered layer list. A multi-arch ref is an INDEX, whose manifests each
// carry a platform — handled by resolveContainerDiskManifest.
type containerDiskManifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`
	// index arm
	Manifests []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Platform  struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

// resolveContainerDiskManifest resolves the PLATFORM image manifest from raw manifest
// JSON: if it is a multi-arch index, the amd64/linux member is selected (matching the
// host this build runs on); a plain manifest yields an empty digest. Pure — it takes the
// bytes `skopeo inspect --raw` already fetched, so the index→manifest selection is the
// one piece of real logic the in-package unit gate can exercise with no registry.
func resolveContainerDiskManifest(rawManifestJSON []byte) (string, string, error) {
	var m containerDiskManifest
	if err := json.Unmarshal(rawManifestJSON, &m); err != nil {
		return "", "", fmt.Errorf("decoding manifest: %w", err)
	}
	if len(m.Manifests) == 0 {
		if m.MediaType == "" && len(m.Layers) == 0 {
			return "", "", fmt.Errorf("not an OCI image manifest (no layers, no manifests)")
		}
		return "", m.MediaType, nil
	}
	// An index: pick amd64/linux, matching the host the VM will run on.
	for _, e := range m.Manifests {
		if e.Platform.OS == "linux" && e.Platform.Architecture == "amd64" {
			return e.Digest, e.MediaType, nil
		}
	}
	return "", "", fmt.Errorf("OCI image index has no linux/amd64 member")
}

// containerDiskLayerDigest returns the single disk layer's digest from a platform
// manifest. A containerDisk is a scratch image whose ONE layer is the disk; a manifest
// with zero layers is a malformed payload and a multi-layer one is not a containerDisk.
func containerDiskLayerDigest(platformManifestJSON []byte) (string, string, error) {
	var m containerDiskManifest
	if err := json.Unmarshal(platformManifestJSON, &m); err != nil {
		return "", "", fmt.Errorf("decoding platform manifest: %w", err)
	}
	if len(m.Layers) == 0 {
		return "", "", fmt.Errorf("containerDisk manifest has no layers (expected one holding the disk)")
	}
	return m.Layers[0].Digest, m.Layers[0].MediaType, nil
}

// isGzipLayer reports whether a layer mediaType is the gzip-compressed tar form. Cua's
// own artifact uses `application/vnd.oci.image.layer.v1.tar+gzip`; a buildah-emitted one
// may be the uncompressed `application/vnd.oci.image.layer.v1.tar`. Both are accepted,
// and the mediaType is what decides whether to wrap the reader in a gzip decoder —
// sniffing the bytes would be a second, divergent source of truth.
func isGzipLayer(mediaType string) bool {
	return strings.Contains(mediaType, "+gzip")
}

// extractDiskFromLayerTar streams an OCI layer (a tar, optionally gzip-compressed) and
// writes the FIRST entry whose cleaned name equals want (normalised without a leading
// slash) to destPath. Entries are matched on the whole path, so a `disk/disk.img` in the
// archive satisfies want `/disk/disk.img`. Returns an error naming the wanted path when
// it is absent — a missing disk is the one failure that would otherwise surface much
// later, as a VM that will not boot.
func extractDiskFromLayerTar(r io.Reader, gzipped bool, want, destPath string) error {
	src := r
	if gzipped {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("opening gzip layer: %w", err)
		}
		defer func() { _ = gz.Close() }()
		src = gz
	}
	wantClean := strings.TrimPrefix(filepath.Clean("/"+want), "/")
	tr := tar.NewReader(src)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading layer tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if strings.TrimPrefix(filepath.Clean(hdr.Name), "/") != wantClean {
			continue
		}
		out, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("creating %s: %w", destPath, err)
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // trusted registry artifact
			_ = out.Close()
			return fmt.Errorf("writing %s: %w", destPath, err)
		}
		return out.Close()
	}
	return fmt.Errorf("layer has no entry %q (is this a containerDisk with the disk at %s?)", want, want)
}

// defaultContainerDiskPath is the KubeVirt contract: the directory KubeVirt scans when
// the VMI names no custom `path:`. Kept in lockstep with the spec arm's comment.
const defaultContainerDiskPath = "/disk/disk.img"

// containerDiskInImagePath returns the source's disk path, defaulting to the KubeVirt
// contract when unauthored.
func containerDiskInImagePath(src VmSource) string {
	if src.DiskPathInImage != "" {
		return src.DiskPathInImage
	}
	return defaultContainerDiskPath
}

// containerDiskCacheDir is the content-addressed cache directory for a containerDisk,
// keyed by the image ref's digest (or, for a mutable tag, a stable hash of the ref).
func containerDiskCacheDir(src VmSource) (string, error) {
	root := src.Cache
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home dir: %w", err)
		}
		root = filepath.Join(home, ".cache", "charly", "vm-images", "container-disks")
	}
	key := src.Image
	if i := strings.LastIndex(key, "@"); i >= 0 {
		key = key[i+1:] // the digest, when pinned
	} else {
		sum := sha256.Sum256([]byte(src.Image))
		key = hex.EncodeToString(sum[:])
	}
	return filepath.Join(root, strings.NewReplacer(":", "-", "/", "-").Replace(key)), nil
}

// containerDiskCacheIdentity is the artifact identity recorded in the cache marker and
// recomputed for the hit test. A resolved platform digest IS the identity; a PLAIN manifest
// (platformDigest == "") has no self-digest, so the raw manifest bytes are hashed. Pure, so
// the hit/miss contract is unit-testable with no registry: a re-pull of an unchanged
// artifact computes the same identity and is a HIT.
func containerDiskCacheIdentity(platformDigest string, rawManifestJSON []byte) string {
	if platformDigest != "" {
		return platformDigest
	}
	sum := sha256.Sum256(rawManifestJSON)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resolveLayoutBlob returns the on-disk path of the blob for `digest` in a layout directory
// produced by `skopeo copy … dir:<dir>`. The `dir:` transport writes blobs named by the BARE
// hex digest (measured on skopeo 1.14: sha256:07b912… → ./07b912…), whereas an OCI image
// layout uses blobs/sha256/<hex>. Try each, and fall back to scanning for a file whose name
// contains the hex — so a transport-layout change fails loudly at ONE seam instead of silently
// after a full registry round-trip.
func resolveLayoutBlob(dir, digest string) (string, error) {
	hex := strings.TrimPrefix(digest, "sha256:")
	candidates := []string{
		filepath.Join(dir, hex),                                  // skopeo dir: transport
		filepath.Join(dir, strings.Replace(digest, ":", "-", 1)), // alt: sha256-<hex>
		filepath.Join(dir, "blobs", "sha256", hex),               // OCI image layout
		filepath.Join(dir, digest),                               // rare: sha256:<hex>
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c, nil
		}
	}
	// Last resort: any entry whose name ends with the hex (covers prefixed layouts).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), hex) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("layer blob for %s not found in layout %s (looked for %v)", digest, dir, candidates)
}

// skopeoRawManifest returns the raw manifest JSON for an OCI ref via `skopeo inspect
// --raw`, honouring `--override-arch amd64` so a multi-arch index resolves to the
// platform this build targets. It is a package var so the unit gate can inject a fixture
// without a registry.
var skopeoRawManifest = func(image string) ([]byte, error) {
	cmd := exec.Command("skopeo", "inspect", "--raw", "--override-os", "linux", "--override-arch", "amd64", "docker://"+image)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		msg := ""
		if errors.As(err, &exit) {
			msg = strings.TrimSpace(string(exit.Stderr))
		}
		// A bare multi-arch index digest needs no override; fall back without it.
		cmd2 := exec.Command("skopeo", "inspect", "--raw", "docker://"+image)
		if out2, err2 := cmd2.Output(); err2 == nil {
			return out2, nil
		}
		return nil, fmt.Errorf("skopeo inspect --raw %s: %w: %s", image, err, msg)
	}
	return out, nil
}

// skopeoCopyToDir copies one image into a `dir:` transport layout, from which the layer
// blob is read (see resolveLayoutBlob for the blob-name forms).
func skopeoCopyToDir(image, dir string) error {
	cmd := exec.Command("skopeo", "copy", "--override-os", "linux", "--override-arch", "amd64",
		"docker://"+image, "dir:"+dir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("skopeo copy %s: %w", image, err)
	}
	return nil
}

// BuildContainerDisk pulls the OCI artifact named by source.image, extracts the guest
// disk to <outputDir>/disk.qcow2, and re-renders the seed ISO — producing the same
// (diskPath, seedIsoPath) pair BuildCloudImage does, so the VM create path is unchanged.
//
// The extracted disk is cached under the content-addressed cache keyed by the manifest
// digest; an unchanged artifact is a cache HIT and no re-pull happens. The base disk is
// then copied to the output dir (the per-VM working disk the deploy snapshots and boots).
func BuildContainerDisk(vmspec *VmSpec, outputDir, vmStateDir string, existingState *VmDeployState, force bool) (CloudImageBuildResult, error) {
	if vmspec.Source.Kind != "container_disk" {
		return CloudImageBuildResult{}, fmt.Errorf("BuildContainerDisk called with source.kind=%q (expected container_disk)", vmspec.Source.Kind)
	}
	if vmspec.Source.Image == "" {
		return CloudImageBuildResult{}, fmt.Errorf("source.image is required for a container_disk VM")
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return CloudImageBuildResult{}, fmt.Errorf("creating output dir: %w", err)
	}
	diskPath := filepath.Join(outputDir, "disk.qcow2")
	seedPath := filepath.Join(outputDir, "seed.iso")

	cacheDir, err := containerDiskCacheDir(vmspec.Source)
	if err != nil {
		return CloudImageBuildResult{}, err
	}
	cachedDisk := filepath.Join(cacheDir, "disk.qcow2")
	digestMarker := filepath.Join(cacheDir, "manifest-digest.txt")

	// The build signature: the source image ref + the in-image path. A changed ref or
	// path invalidates the per-VM disk; the content cache is separately digest-keyed.
	sig := vmBuildStamp{BaseSHA256: vmspec.Source.Image, DiskSize: vmspec.DiskSize, SourceURL: containerDiskInImagePath(vmspec.Source)}
	if !force && diskBaseFresh(outputDir, diskPath, sig) {
		fmt.Fprintf(os.Stderr, "Container-disk VM disk %s is content-fresh — skipping re-pull\n", diskPath)
	} else {
		if err := pullContainerDisk(vmspec.Source, cacheDir, cachedDisk, digestMarker, force); err != nil {
			return CloudImageBuildResult{}, err
		}
		// Materialise the per-VM working disk. A copy (not an overlay) because the cached
		// base is shared read-only across VMs and the deploy mutates the working disk
		// (snapshots, guest writes); copying keeps the cache pristine.
		_ = os.Remove(diskPath)
		if err := copyFile(cachedDisk, diskPath); err != nil {
			return CloudImageBuildResult{}, err
		}
		if vmspec.DiskSize != "" {
			if err := qemuImgResize(diskPath, vmspec.DiskSize); err != nil {
				return CloudImageBuildResult{}, err
			}
		}
		if err := writeVmBuildStamp(outputDir, sig); err != nil {
			return CloudImageBuildResult{}, fmt.Errorf("writing build stamp: %w", err)
		}
	}

	// --- Seed ISO: identical to the cloud_image path (the arm is cloud_image-like). ---
	instanceID := ""
	if existingState != nil && existingState.InstanceID != "" {
		instanceID = existingState.InstanceID
	} else {
		instanceID = newUUID4()
	}
	_, cloudInitEnabled := ResolveKeyInjectionChannels(vmspec)
	pubKey, err := resolveSSHPubKeyForSpec(vmspec, vmStateDir)
	if err != nil {
		return CloudImageBuildResult{}, fmt.Errorf("resolving ssh pubkey: %w", err)
	}
	hostname := ""
	if vmspec.CloudInit != nil {
		hostname = vmspec.CloudInit.Hostname
	}
	rt := CloudInitRuntimeParams{
		SSHPublicKey:          pubKey,
		InstanceID:            instanceID,
		Hostname:              hostname,
		InjectKeyViaCloudInit: cloudInitEnabled,
	}
	userData, metaData, networkConfig, err := RenderCloudInit(vmspec, rt)
	if err != nil {
		return CloudImageBuildResult{}, fmt.Errorf("rendering cloud-init: %w", err)
	}
	digest := sha256.Sum256([]byte(userData))
	if err := WriteSeedISO(seedPath, userData, metaData, networkConfig); err != nil {
		return CloudImageBuildResult{}, fmt.Errorf("writing seed iso: %w", err)
	}

	return CloudImageBuildResult{
		DiskPath:        diskPath,
		SeedIsoPath:     seedPath,
		InstanceID:      instanceID,
		BaseImageSHA256: vmspec.Source.Image,
		CloudInitDigest: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

// pullContainerDisk ensures the content-addressed cache holds the extracted disk for
// source.image. A present cache with a matching recorded manifest digest is a hit; a
// miss pulls the artifact and extracts the disk. The work runs under a per-cache-dir
// flock (two beds building the same image must not interleave) with an atomic rename of
// the finished disk into place, mirroring kit.FetchQcow2's discipline.
func pullContainerDisk(src VmSource, cacheDir, cachedDisk, digestMarker string, force bool) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("creating container-disk cache %s: %w", cacheDir, err)
	}
	release, err := lockPath(cachedDisk)
	if err != nil {
		return fmt.Errorf("container-disk fetch lock: %w", err)
	}
	defer release()

	raw, err := skopeoRawManifest(src.Image)
	if err != nil {
		return err
	}
	platformDigest, _, err := resolveContainerDiskManifest(raw)
	if err != nil {
		return err
	}
	// The artifact IDENTITY for the cache marker. An index resolves to a platform manifest
	// (its digest identifies the amd64 image); a PLAIN manifest carries no self-digest, so
	// hash the raw manifest bytes. Either way the value is derived from what we already
	// fetched, so the hit test and the marker write compute the SAME identity — which the
	// previous code did not (it wrote the layer digest but compared the ref).
	identity := containerDiskCacheIdentity(platformDigest, raw)
	// Cache hit: the recorded identity matches and the disk is present.
	if !force && fileExists(cachedDisk) {
		if recorded, rerr := os.ReadFile(digestMarker); rerr == nil {
			if strings.TrimSpace(string(recorded)) == identity {
				return nil
			}
		}
	}

	// Resolve the platform manifest when the ref was an index.
	manifestJSON := raw
	if platformDigest != "" && !strings.Contains(src.Image, platformDigest) {
		manifestJSON, err = skopeoRawManifest(strings.SplitN(src.Image, "@", 2)[0] + "@" + platformDigest)
		if err != nil {
			return err
		}
	}
	layerDigest, layerMedia, err := containerDiskLayerDigest(manifestJSON)
	if err != nil {
		return err
	}

	// Fetch the layer via a directory OCI layout, then stream the disk out of it.
	layoutDir, err := os.MkdirTemp(cacheDir, "pull-*")
	if err != nil {
		return fmt.Errorf("creating pull dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(layoutDir) }()
	if err := skopeoCopyToDir(src.Image, layoutDir); err != nil {
		return err
	}

	// Locate the layer blob. skopeo's `dir:` transport names blobs by the BARE hex digest
	// (the measured layout: a 554-byte file literally named 07b912eb… for sha256:07b912eb…),
	// while an OCI layout uses blobs/sha256/<hex>. Try each known form rather than assuming
	// one — the live pull test is what caught the original single-form assumption.
	layerBlob, err := resolveLayoutBlob(layoutDir, layerDigest)
	if err != nil {
		return err
	}
	f, err := os.Open(layerBlob)
	if err != nil {
		return fmt.Errorf("opening layer blob %s: %w", layerBlob, err)
	}
	defer func() { _ = f.Close() }()

	tmpDisk := cachedDisk + ".part"
	_ = os.Remove(tmpDisk)
	if err := extractDiskFromLayerTar(f, isGzipLayer(layerMedia), containerDiskInImagePath(src), tmpDisk); err != nil {
		_ = os.Remove(tmpDisk)
		return err
	}
	// The disk inside a containerDisk may itself be compressed (Cua's is a qcow2 with
	// internal zlib compression, which qemu reads directly — no action needed); but a
	// gzipped raw disk would not. qemu-img identifies the format; we assert it is qcow2
	// or raw rather than booting something that is neither.
	if err := assertDiskFormat(tmpDisk); err != nil {
		_ = os.Remove(tmpDisk)
		return err
	}
	if err := os.Rename(tmpDisk, cachedDisk); err != nil {
		return fmt.Errorf("promoting extracted disk: %w", err)
	}
	// Record the SAME identity the hit test recomputes (see the top of this function).
	if err := os.WriteFile(digestMarker, []byte(identity+"\n"), 0o644); err != nil {
		return fmt.Errorf("recording manifest digest: %w", err)
	}
	return nil
}

// assertDiskFormat runs `qemu-img info` and rejects a payload qemu cannot boot as a
// disk (neither qcow2 nor raw), naming the actual format — a containerDisk whose layer
// holds something else would otherwise fail at VM start with an opaque libvirt error.
func assertDiskFormat(path string) error {
	out, err := exec.Command("qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return fmt.Errorf("qemu-img info %s: %w", path, err)
	}
	var info struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("decoding qemu-img info: %w", err)
	}
	switch info.Format {
	case "qcow2", "raw":
		return nil
	default:
		return fmt.Errorf("containerDisk layer holds a %q image, not a bootable qcow2/raw disk", info.Format)
	}
}

// copyFile copies src to dst (streaming; the disks are multi-GB).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copying to %s: %w", dst, err)
	}
	return out.Close()
}

// fileExists is a small presence check.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// lockPath takes a cross-process exclusive flock on <path>.lock and returns the
// release. Two `vm build`s of the same containerDisk (parallel beds) must not interleave
// their pulls and atomic renames; this is the plugin-local twin of the sdk's
// per-image fetch lock, which is package-private to sdk/kit.
func lockPath(path string) (func(), error) {
	lockFile := path + ".lock"
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockFile, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("locking %s: %w", lockFile, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
