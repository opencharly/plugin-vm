package vm

// vm_box_emit.go — the box-emission step of `charly vm build` (cutover plan
// task 3, the plugin-vm half of the generic VM box). After every source-kind
// build materializes a disk, this step wraps the disk + the resolved entity's
// metadata into a VM box image in local container-engine storage: an OCI image
// whose labels carry the spec.VmBoxMetadata contract (whole-struct JSON on
// spec.LabelVmBox) and whose single layer carries the disk artifact. The emitter
// is deploykit.EmitVmBoxAt; its read-back side (deploykit.VmCapabilitiesFromLabels)
// is what a `charly deploy from-box vm:<ref>` consumes (cutover task 5).
//
// Two in-image disk layouts ship, chosen by the caller:
//   - the DEFAULT VM-box path /disk.qcow2 (deploykit.VmBoxDiskPath) — the
//     from-box vm: layout;
//   - the KubeVirt containerDisk contract /disk/disk.img
//     (deploykit.ContainerDiskPath), the layout a KubeVirt cluster boots
//     directly (VMI `containerDisk:`), selected by `charly vm build
//     --container-disk` (plan WS-6.3, the charly-box → containerDisk payload).
//
// `charly vm build --push <ref>` additionally publishes the emitted box to a
// registry-pullable ref (the delivery half of WS-6.3); the emit itself stays
// best-effort (a failed emit never fails the disk build) UNLESS a push was
// requested — an explicit delivery request makes both steps load-bearing.

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// buildVmBoxMetadata maps a resolved VmSpec + box name onto the VM box metadata
// contract (spec.VmBoxMetadata). Pure function — no engine/disk access — so the
// field mapping is unit-testable without podman.
//
// Field sources (all best-effort where the source kind does not carry them):
//   - Distro: the source's distro, when the kind declares one (cloud_image /
//     bootstrap / iso). bootc and clone arms have NO source.distro field, and
//     reading the distro out of a box image is the BoxRef resolver's job
//     (cutover task 4, R3 — no duplicated label-read machinery here), so those
//     boxes emit with an empty distro rather than a guessed one.
//   - Arch: the host GOARCH (the disk was built on this host for this host).
//   - BaseUser: the cloud_image adoption account (source.base_user); empty for
//     arms that do not adopt one.
//   - SSHUser / CharlyInstall / Firmware: the entity's own resolved values.
//   - Version: the current CalVer (spec.ComputeCalVer, the repo's YYYY.DDD.HHMM
//     convention) — also the image tag, so each build tags the box with its
//     build instant.
//   - Source: provenance straight from the resolved source (kind + the arm's
//     from_vm/from_snapshot/box/url fields).
func buildVmBoxMetadata(vmName string, vmSpec *VmSpec) *spec.VmBoxMetadata {
	meta := &spec.VmBoxMetadata{
		Distro:   vmSpec.Source.Distro,
		Arch:     runtime.GOARCH,
		BaseUser: vmSpec.Source.BaseUser,
		Firmware: vmSpec.Firmware,
		Version:  spec.ComputeCalVer(),
		Source: spec.VmBoxSource{
			Kind:         vmSpec.Source.Kind,
			FromVm:       vmSpec.Source.FromVm,
			FromSnapshot: vmSpec.Source.FromSnapshot,
			Box:          vmSpec.Source.Box,
			URL:          vmSpec.Source.URL,
		},
		Description: fmt.Sprintf("charly VM box for %s", vmName),
	}
	if vmSpec.SSH != nil {
		meta.SSHUser = vmSpec.SSH.User
	}
	if vmSpec.CloudInit != nil && vmSpec.CloudInit.CharlyInstall != nil {
		meta.CharlyInstall = vmSpec.CloudInit.CharlyInstall.Strategy
	}
	return meta
}

// emitVmBox wraps a materialized disk + the entity's metadata into a VM box
// image in local engine storage and tags it
// `localhost/charly-<vmName>:<calver>` — the local-storage convention the
// bootc images already use. It returns the box ref so the caller can print it.
//
// inImagePath selects where the disk lands INSIDE the image: the empty string
// uses the default VM-box path (deploykit.VmBoxDiskPath, /disk.qcow2);
// deploykit.ContainerDiskPath (/disk/disk.img) emits the KubeVirt containerDisk
// layout a cluster boots directly. The path is validated by EmitVmBoxAt.
//
// The engine string is the drive's resolved engine (reply.Engine — "podman" on
// this host). The error is returned unwrapped so the caller decides how to
// surface it (the drive warns and keeps the disk build's success).
func emitVmBox(engine, vmName string, vmSpec *VmSpec, diskPath, inImagePath string) (string, error) {
	if vmSpec == nil {
		return "", fmt.Errorf("emitVmBox: nil vm spec")
	}
	meta := buildVmBoxMetadata(vmName, vmSpec)
	ref := fmt.Sprintf("localhost/charly-%s:%s", vmName, meta.Version)
	path := inImagePath
	if path == "" {
		path = deploykit.VmBoxDiskPath
	}
	if err := deploykit.EmitVmBoxAt(engine, ref, meta, diskPath, path); err != nil {
		return "", fmt.Errorf("emitting VM box %s: %w", ref, err)
	}
	// A STABLE `:latest` handle alongside the CalVer tag: the CalVer is wall-clock and
	// unknowable to a consumer that must NAME the box statically — a kind:kubevirt
	// `containerDisk.image`, or any authored ref. With both tags on the one image, a
	// consumer names `localhost/charly-<vm>:latest` and a registry push carries both.
	// ONE derivation (latestRef) shared with the --push path, so the two cannot diverge.
	stable := latestRef(ref)
	if stable != ref {
		if err := retagImage(engine, ref, stable); err != nil {
			return "", fmt.Errorf("tagging the stable box ref %s: %w", stable, err)
		}
	}
	return ref, nil
}

// boxInImagePath maps the `--container-disk` choice onto the in-image path the
// emitter writes: the KubeVirt containerDisk contract when set, the default
// VM-box layout (empty → the emitter's /disk.qcow2) otherwise. Pure.
func boxInImagePath(containerDisk bool) string {
	if containerDisk {
		return deploykit.ContainerDiskPath
	}
	return ""
}

// engineCmd runs one container-engine subcommand for the box-delivery path. A
// package var so the push argv is unit-testable without a live engine.
var engineCmd = func(binary string, args ...string) error {
	cmd := exec.Command(binary, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", binary, strings.Join(args, " "), err)
	}
	return nil
}

// pushVmBox publishes a locally-emitted VM box to dstRef so a target cluster can
// pull it: it retags the local image to the destination ref (via retagImage — the
// ONE tag primitive; a no-op when the caller already passed the target ref) and
// pushes it with the engine. The retag is local and cheap; the push is the
// delivery.
func pushVmBox(engine, srcRef, dstRef string) error {
	if dstRef == "" {
		return fmt.Errorf("pushVmBox: empty destination ref")
	}
	if srcRef != "" {
		if err := retagImage(engine, srcRef, dstRef); err != nil {
			return err
		}
	}
	if err := engineCmd(container.EngineBinary(engine), "push", dstRef); err != nil {
		return fmt.Errorf("pushing %s: %w", dstRef, err)
	}
	// Publish the STABLE `:latest` handle at the destination too, so a registry consumer
	// that must NAME the box statically (a kind:kubevirt `containerDisk.image`) resolves
	// the same image the CalVer ref does — the registry twin of emitVmBox's local handle.
	if stable := latestRef(dstRef); stable != dstRef {
		if err := retagImage(engine, dstRef, stable); err != nil {
			return err
		}
		if err := engineCmd(container.EngineBinary(engine), "push", stable); err != nil {
			return fmt.Errorf("pushing the stable handle %s: %w", stable, err)
		}
	}
	return nil
}

// latestRef returns ref with its tag replaced by `:latest` (appending `:latest` when ref
// carries no tag) — the destination-side twin of emitVmBox's stable local handle. A
// registry host:port in the first path segment is preserved (only the LAST segment's tag
// is touched); a digest-pinned ref is returned unchanged (a tag would be discarded).
func latestRef(ref string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon] + ":latest"
	}
	return ref + ":latest"
}
