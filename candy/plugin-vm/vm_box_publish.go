package vm

// vm_box_publish.go — `charly vm box publish`: turn a VM's disk (the live disk,
// or a captured snapshot's disk) into a bootable containerDisk OCI image and
// push it to a registry.
//
// Two steps, both reusing machinery plugin-vm already owns:
//
//  1. EMIT a local VM-box image whose single layer carries the disk at the
//     KubeVirt/Cua in-image path (deploykit.ContainerDiskPath = /disk/disk.img),
//     via the SDK emitter (buildah) — the same emitter `charly vm build`/`bake`
//     use for the from-box artifact.
//  2. PUSH it with `skopeo copy --format oci --dest-compress
//     containers-storage:<localRef> docker://<registry>` — the SAME skopeo
//     dependency the container_disk PULL (vm_container_disk.go) uses, in the
//     opposite direction. `--dest-compress` sets the layer media type to
//     application/vnd.oci.image.layer.v1.tar+gzip, the containerDisk contract
//     Cua Fleet / KubeVirt require; buildah's own layer is the uncompressed
//     application/vnd.oci.image.layer.v1.tar.
//
// The pushed manifest is re-read (`skopeo inspect --raw`) and its layer media
// type asserted to be +gzip, so a regression to an uncompressed layer fails the
// command instead of printing a green ref.
//
// This is the produce half of the containerDisk support the `container_disk`
// #VmSource arm consumes; the in-image path and the metadata contract are the
// SDK's single homes (deploykit.ContainerDiskPath + buildVmBoxMetadata).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
)

// containerDiskGzipLayer is the layer media type a containerDisk must carry —
// Cua Fleet's contract (S1/S2), and what `skopeo copy --dest-compress` produces.
const containerDiskGzipLayer = "application/vnd.oci.image.layer.v1.tar+gzip"

// VmBoxCmd groups the VM-box artifact commands.
type VmBoxCmd struct {
	Publish VmBoxPublishCmd `cmd:"" help:"Publish a VM's disk as a bootable containerDisk OCI image (one +gzip layer at /disk/disk.img) to a registry"`
}

// VmBoxPublishCmd implements `charly vm box publish <vm> --to <registry>`.
type VmBoxPublishCmd struct {
	Box          string `arg:"" help:"VM name (the kind:vm entity whose disk is published)"`
	To           string `name:"to" required:"" help:"Registry reference to push (host/repo:tag)"`
	FromSnapshot string `name:"from-snapshot" help:"Publish the disk captured by this snapshot (default: the VM's current disk)"`
	InImagePath  string `name:"in-image-path" help:"In-layer disk path (default the KubeVirt/Cua containerDisk contract, /disk/disk.img)"`
	Insecure     bool   `name:"insecure" help:"Allow a plain-HTTP (localhost dev) registry"`
}

// Run executes charly vm box publish.
func (c *VmBoxPublishCmd) Run() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	vmSpec, err := resolveVmBuildEntity(cmdCtx, cmdExec, dir, c.Box)
	if err != nil {
		return err
	}
	if vmSpec == nil {
		return noVmEntityErr(c.Box)
	}
	disk, err := resolvePublishDisk(c.Box, c.FromSnapshot)
	if err != nil {
		return err
	}
	// The engine is resolved the SAME way the build drive resolves it (the
	// buildah-backed engine binary deploykit.EmitVmBox runs).
	engine := "podman"
	if rt, rerr := kit.ResolveRuntime(); rerr == nil {
		engine = kit.EngineBinary(rt.RunEngine)
	}
	reply, err := publishContainerDisk(engine, c.Box, vmSpec, disk, c.To, c.InImagePath, c.Insecure)
	if err != nil {
		return err
	}
	fmt.Printf("published containerDisk %s@%s\n", reply.Ref, reply.Digest)
	fmt.Printf("  disk:       %s\n", disk)
	fmt.Printf("  media type: %s\n", reply.MediaType)
	fmt.Printf("  local box:  %s\n", reply.LocalRef)
	return nil
}

// resolvePublishDisk returns the host path of the disk to publish: the snapshot's
// captured external disk when fromSnapshot names one, else the VM's live disk.
// A named-but-absent or internal-mode snapshot is a real error rather than a
// silent fall-back to the live disk (which would publish the wrong bytes).
func resolvePublishDisk(box, fromSnapshot string) (string, error) {
	if fromSnapshot != "" {
		entries, err := ListSnapshots(box)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.Name != fromSnapshot {
				continue
			}
			if e.DiskPath == "" {
				return "", fmt.Errorf("vm box publish: snapshot %q of %q is %s-mode and carries no external disk to publish", fromSnapshot, box, e.Mode)
			}
			return e.DiskPath, nil
		}
		return "", fmt.Errorf("vm box publish: %q has no snapshot %q", box, fromSnapshot)
	}
	disk, err := vmDiskPath(box)
	if err != nil {
		return "", fmt.Errorf("vm box publish: resolving the disk of %q: %w", box, err)
	}
	return disk, nil
}

// publishResult is the publish outcome the command prints.
type publishResult struct {
	Ref       string
	Digest    string
	MediaType string
	LocalRef  string
}

// publishContainerDisk emits the local containerDisk box (disk at inImagePath,
// metadata labels) and pushes it to ref with skopeo, then verifies the pushed
// layer is the +gzip form.
func publishContainerDisk(engine, vmName string, vmSpec *VmSpec, diskPath, ref, inImagePath string, insecure bool) (publishResult, error) {
	if inImagePath == "" {
		inImagePath = deploykit.ContainerDiskPath
	}
	meta := buildVmBoxMetadata(vmName, vmSpec)
	localRef := containerDiskLocalRef(vmName, meta.Version)
	if err := deploykit.EmitVmBoxAt(engine, localRef, meta, diskPath, inImagePath); err != nil {
		return publishResult{}, fmt.Errorf("vm box publish: emitting the local containerDisk box %s: %w", localRef, err)
	}
	if out, err := exec.Command("skopeo", skopeoCopyArgs(localRef, ref, insecure)...).CombinedOutput(); err != nil {
		return publishResult{}, fmt.Errorf("vm box publish: skopeo copy %s -> %s: %w: %s", localRef, ref, err, strings.TrimSpace(string(out)))
	}
	raw, err := exec.Command("skopeo", skopeoInspectRawArgs(ref, insecure)...).Output()
	if err != nil {
		return publishResult{}, fmt.Errorf("vm box publish: inspecting the pushed %s: %w", ref, err)
	}
	mt, err := containerDiskMediaType(raw)
	if err != nil {
		return publishResult{}, fmt.Errorf("vm box publish: %w", err)
	}
	if mt != containerDiskGzipLayer {
		return publishResult{}, fmt.Errorf("vm box publish: pushed layer media type %q (want %q — the containerDisk +gzip contract)", mt, containerDiskGzipLayer)
	}
	digestOut, derr := exec.Command("skopeo", skopeoInspectDigestArgs(ref, insecure)...).Output()
	if derr != nil {
		return publishResult{}, fmt.Errorf("vm box publish: reading the pushed digest: %w", derr)
	}
	return publishResult{Ref: ref, Digest: strings.TrimSpace(string(digestOut)), MediaType: mt, LocalRef: localRef}, nil
}

// containerDiskLocalRef is the local engine tag the containerDisk box is emitted
// under. It is DISTINCT from the from-box VM-box ref (localhost/charly-<vm>:<v>,
// whose disk lives at /disk.qcow2), so the two artifacts never collide.
func containerDiskLocalRef(vmName, calver string) string {
	return fmt.Sprintf("localhost/charly-%s-containerdisk:%s", vmName, calver)
}

// skopeoCopyArgs is the exact push argv: OCI manifest type, gzip-compressed
// destination layers (the +gzip contract), and TLS disabled for an insecure
// (plain-HTTP) registry.
func skopeoCopyArgs(localRef, to string, insecure bool) []string {
	args := []string{"copy", "--format", "oci", "--dest-compress", "--dest-compress-format", "gzip"}
	if insecure {
		args = append(args, "--dest-tls-verify=false")
	}
	return append(args, "containers-storage:"+localRef, "docker://"+to)
}

// skopeoInspectRawArgs fetches the pushed manifest JSON.
func skopeoInspectRawArgs(ref string, insecure bool) []string {
	args := []string{"inspect", "--raw"}
	if insecure {
		args = append(args, "--tls-verify=false")
	}
	return append(args, "docker://"+ref)
}

// skopeoInspectDigestArgs fetches the pushed manifest digest.
func skopeoInspectDigestArgs(ref string, insecure bool) []string {
	args := []string{"inspect", "--format", "{{.Digest}}"}
	if insecure {
		args = append(args, "--tls-verify=false")
	}
	return append(args, "docker://"+ref)
}

// containerDiskMediaType is the PURE manifest guard: parse the pushed OCI
// manifest and return its single disk layer's media type. An index (no layers)
// or a multi-layer manifest is not a containerDisk and is an error.
func containerDiskMediaType(rawManifest []byte) (string, error) {
	var m struct {
		Layers []struct {
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(rawManifest, &m); err != nil {
		return "", fmt.Errorf("decoding the pushed manifest: %w", err)
	}
	if len(m.Layers) != 1 {
		return "", fmt.Errorf("pushed manifest has %d layers (a containerDisk is a scratch image with exactly one)", len(m.Layers))
	}
	return m.Layers[0].MediaType, nil
}
