package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// vm_import_container_disk.go — Kong wiring for `charly vm import-container-disk <ref>`.
//
// A containerDisk is an OCI artifact carrying a bootable guest disk (the KubeVirt
// containerDisk contract: the disk lives in a layer, by default at /disk/disk.img).
// charly's `container_disk` VM source (spec#171) pulls such an artifact while BUILDING a
// `kind: vm` entity. This command is the standalone primitive that pull is built on: it
// imports the artifact into the SHARED, content-addressed VM-image cache — WITHOUT
// authoring a VM entity — so a later `charly vm build` of a `container_disk` entity that
// names the SAME ref is a cache hit (the pull is done once), and an operator can pre-warm
// a large Fleet image in CI or before a bed run.
//
// The cache key is the shared engine's (containerDiskCacheDir): a DIGEST-pinned ref
// (`reg/x@sha256:…`) keys by the digest (`sha256-<hex>`), a TAG ref (`reg/x:1`) keys by
// the sha256 of the ref string. A digest-pinned import therefore shares one entry with a
// digest-pinned build; the same artifact named by tag and by digest gets two entries.
//
// It reuses the ONE pull engine (pullContainerDisk) the build drive already calls — no
// second fetch path, no duplicated index→manifest resolution (R3).

// VmImportContainerDiskCmd implements `charly vm import-container-disk <ref>`.
type VmImportContainerDiskCmd struct {
	// Image is the containerDisk OCI ref to import. A DIGEST pin gives a stable cache
	// identity shared across consumers; a tag keys the cache by the ref string (see the
	// package comment).
	Image string `arg:"" help:"containerDisk OCI ref to import (e.g. public.ecr.aws/k5j5w0x5/cua-omarchy-workspace@sha256:…)"`

	// DiskPathInImage overrides the in-image disk path (default /disk/disk.img, the
	// KubeVirt containerDisk scan directory).
	DiskPathInImage string `name:"disk-path-in-image" help:"Disk path INSIDE the image (default /disk/disk.img, the KubeVirt containerDisk contract)"`

	// Cache overrides the cache root (default ~/.cache/charly/vm-images/container-disks).
	Cache string `name:"cache" help:"Override the container-disk cache root (default ~/.cache/charly/vm-images/container-disks)"`

	// Force re-pulls even when the cache already holds the artifact.
	Force bool `name:"force" help:"Re-pull even when the cache already holds the artifact"`
}

// containerDiskImportSource synthesizes the #VmSource the import pulls through — the SAME
// shape a `container_disk` entity carries, so the cache key (containerDiskCacheDir) and
// the pull engine (pullContainerDisk) are shared with the build path by construction.
// Pure, so the synthesis is unit-testable without a registry.
func containerDiskImportSource(image, diskPathInImage, cache string) VmSource {
	return VmSource{
		Kind:            "container_disk",
		Image:           image,
		DiskPathInImage: diskPathInImage,
		Cache:           cache,
	}
}

// Run executes `charly vm import-container-disk`.
func (c *VmImportContainerDiskCmd) Run() error {
	if c.Image == "" {
		return fmt.Errorf("charly vm import-container-disk: a containerDisk ref is required")
	}
	src := containerDiskImportSource(c.Image, c.DiskPathInImage, c.Cache)

	cacheDir, err := containerDiskCacheDir(src)
	if err != nil {
		return err
	}
	cachedDisk := filepath.Join(cacheDir, "disk.qcow2")
	digestMarker := filepath.Join(cacheDir, "manifest-digest.txt")

	if err := pullContainerDisk(src, cacheDir, cachedDisk, digestMarker, c.Force); err != nil {
		return err
	}

	var size int64
	if fi, serr := os.Stat(cachedDisk); serr == nil {
		size = fi.Size()
	}
	identity := ""
	if b, rerr := os.ReadFile(digestMarker); rerr == nil {
		identity = strings.TrimSpace(string(b))
	}
	fmt.Printf("imported containerDisk %s\n", c.Image)
	fmt.Printf("  disk:     %s (%d bytes)\n", cachedDisk, size)
	fmt.Printf("  identity: %s\n", identity)
	fmt.Printf("  cache:    %s\n", cacheDir)
	return nil
}
