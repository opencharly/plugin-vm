package vm

// vm_box_emit.go — the box-emission step of `charly vm build` (cutover plan
// task 3, the plugin-vm half of the generic VM box). After every source-kind
// build materializes a disk, this step wraps the disk + the resolved entity's
// metadata into a VM box image in local container-engine storage: an OCI image
// whose labels carry the spec.VmBoxMetadata contract (whole-struct JSON on
// spec.LabelVmBox) and whose single layer carries the disk artifact at
// /disk.qcow2. The emitter is deploykit.EmitVmBox (sdk PR #202); its read-back
// side (deploykit.VmCapabilitiesFromLabels) is what a future
// `charly fleet from-box vm:<ref>` consumes (cutover task 5).
//
// Best-effort by contract: the disk build is the primary artifact and the box
// is its metadata wrapper, so a missing engine or a failed emit only warns and
// leaves the build green — the drive never fails on box emission.

import (
	"fmt"
	"runtime"

	"github.com/opencharly/sdk/deploykit"
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
// bootc images already use (CalVer-tagged, never pushed by the build). It
// returns the box ref so the caller can print it.
//
// The engine string is the drive's resolved engine (reply.Engine — "podman" on
// this host). deploykit.EmitVmBox runs the engine build; the error is returned
// unwrapped so the caller decides how to surface it (the drive warns and keeps
// the disk build's success).
func emitVmBox(engine, vmName string, vmSpec *VmSpec, diskPath string) (string, error) {
	if vmSpec == nil {
		return "", fmt.Errorf("emitVmBox: nil vm spec")
	}
	meta := buildVmBoxMetadata(vmName, vmSpec)
	ref := fmt.Sprintf("localhost/charly-%s:%s", vmName, meta.Version)
	if err := deploykit.EmitVmBox(engine, ref, meta, diskPath); err != nil {
		return "", fmt.Errorf("emitting VM box %s: %w", ref, err)
	}
	return ref, nil
}
