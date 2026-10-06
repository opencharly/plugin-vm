package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// vm_build.go — the command:vm `charly vm build` DRIVE (P8b-rest: the disk-build ENGINE moved HERE
// from charly core — the same inversion candy/plugin-build's podman DRIVE already went through in
// P8b). PREP+RESOLVE now ALSO runs plugin-side (K3 vm-build move, coneB-buildremnant): resolveVmBuild
// (vm_build_resolve.go) resolves the kind:vm entity + the build vocabulary + the per-source-kind
// image refs into the spec.VmBuildReply envelope entirely in-process — the former hidden core
// HostBuild("vm-build") reentry (charly/host_build_vm_build.go) is DELETED, its LoadUnified /
// LoadBuildConfigForBox / resolveBootcImageRef / ensureBuilderImageBuilt Mechanisms all now reached
// through plugin-callable seams (loaderkit.LoadUnified, InvokeProvider(kind, local/distro),
// InvokeProvider(build, box)) instead of a host round-trip. This command runs the actual
// privileged-container / qemu-img / bootc-install / cloud-init exec itself and prints its own
// progress to the shared stdio (compiled-in, so os.Stderr is the operator's terminal).
type VmBuildCmd struct {
	Box           string `arg:"" help:"Bootc image name"`
	Size          string `name:"size" help:"Override disk size (e.g. 20G, '20 GiB')"`
	RootSize      string `name:"root-size" help:"Override root partition size (e.g. 10G)"`
	Tag           string `name:"tag" help:"Image tag override"`
	Type          string `name:"type" default:"qcow2" help:"Output format: qcow2, raw"`
	Transport     string `name:"transport" help:"Image transport: registry, containers-storage, oci, oci-archive"`
	Console       bool   `name:"console" help:"Enable console output for debugging"`
	Force         bool   `name:"force" help:"Rebuild the disk base even when content-fresh (default: skip if the base already matches the source). SINGLE-BED ONLY — do NOT force-rebuild a base that live per-domain overlays back onto (it mutates a read-only backing file); the concurrent-bed R10 uses idempotent-skip, never --force."`
	FromSnapshot  string `name:"from-snapshot" help:"Build the entity's disk as a CLONE of its own golden at this snapshot (the unified from: name:tag functional half — the deploy's from_snapshot flows here)."`
	ContainerDisk bool   `name:"container-disk" help:"Emit the VM box with the disk at /disk/disk.img (the KubeVirt containerDisk contract a cluster boots directly) instead of the default /disk.qcow2."`
	Push          string `name:"push" help:"After emitting, retag and push the VM box image to this registry-pullable ref (the WS-6.3 delivery half). An explicit push makes the emit and the push load-bearing."`
}

func (c *VmBuildCmd) Run() error {
	switch c.Type {
	case "qcow2", "raw":
	case "iso":
		return fmt.Errorf("iso format is not supported — use qcow2 or raw")
	default:
		return fmt.Errorf("unsupported disk type %q (valid: qcow2, raw)", c.Type)
	}
	return vmBuildDrive(c.Box, spec.VmBuildRequest{
		Box: c.Box, Size: c.Size, RootSize: c.RootSize, Tag: c.Tag,
		Type: c.Type, Transport: c.Transport, Console: c.Console, Force: c.Force,
		FromSnapshot: c.FromSnapshot,
	}, c.emitOpts())
}

// vmBuildDrive is the drive seam for `charly vm build`: a package var so the
// command's flag → opts → drive wiring is unit-testable without a host reverse
// channel. It is the ONE caller of runVmBuildDrive.
var vmBuildDrive = runVmBuildDrive

// emitOpts maps the command's box-emission flags onto the drive's options.
// Pure, so the flag wiring is unit-testable.
func (c *VmBuildCmd) emitOpts() vmBoxEmitOpts {
	return vmBoxEmitOpts{ContainerDisk: c.ContainerDisk, Push: c.Push}
}

// vmBoxEmitOpts carries the box-emission choices that are NOT part of the
// spec.VmBuildRequest wire (they select the emitted box layout + delivery, a
// plugin-local concern): ContainerDisk emits the KubeVirt /disk/disk.img layout,
// Push publishes the emitted box to a registry-pullable ref.
type vmBoxEmitOpts struct {
	ContainerDisk bool
	Push          string
}

// runVmBuildDrive runs the standard `charly vm build` pipeline for one entity:
// resolve → per-entity flock → per-source-kind dispatch → box emission. It is
// the single drive behind the `charly vm build` command (R3 — one drive, no
// duplicated dispatch).
func runVmBuildDrive(box string, req spec.VmBuildRequest, emit vmBoxEmitOpts) error {
	if cmdExec == nil {
		return fmt.Errorf("vm build: no host reverse channel (command not compiled-in?)")
	}
	reply, err := resolveVmBuild(cmdCtx, cmdExec, req)
	if err != nil {
		return err
	}

	var vmSpec VmSpec
	if err := json.Unmarshal(reply.VmJSON, &vmSpec); err != nil {
		return fmt.Errorf("decoding resolved vm spec: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Building VM %q (source.kind=%s)\n", box, reply.SourceKind)

	// Per-ENTITY build flock: serialize concurrent `vm build <entity>` so N beds sharing this
	// entity's disk base never race on <vm.image_dir>/<entity>/ (and a second build never
	// rewrites a base a live per-domain overlay backs onto). Blocking — the first builds, the
	// rest wait then idempotent-skip. Released on return (BEFORE `vm create`), so per-domain
	// overlay-creates stay unserialized.
	unlock, lockErr := kit.AcquireFileLock(filepath.Join(reply.OutputDir, ".build.lock"), true)
	if lockErr != nil {
		return fmt.Errorf("acquiring vm build lock for %s: %w", box, lockErr)
	}
	defer func() { _ = unlock() }()

	var builtDisk string
	switch reply.SourceKind {
	case "cloud_image":
		res, err := BuildCloudImage(&vmSpec, reply.OutputDir, reply.VmStateDir, reply.ExistingState, reply.Force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote %s (base sha256=%s)\n", res.DiskPath, res.BaseImageSHA256)
		fmt.Fprintf(os.Stderr, "Wrote %s\n", res.SeedIsoPath)
		fmt.Fprintf(os.Stderr, "Instance-id: %s\n", res.InstanceID)
		builtDisk = res.DiskPath

	case "bootc":
		res, err := BuildBootcVM(&vmSpec, reply.OutputDir, reply.VmStateDir, reply.ExistingState, reply.BootcImageRef)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote %s\n", res.DiskPath)
		if res.SeedIsoPath != "" {
			fmt.Fprintf(os.Stderr, "Wrote %s\n", res.SeedIsoPath)
		}
		builtDisk = res.DiskPath

	case "bootstrap":
		var distro DistroDef
		if err := json.Unmarshal(reply.DistroJSON, &distro); err != nil {
			return fmt.Errorf("decoding resolved distro: %w", err)
		}
		var builder BuilderDef
		if err := json.Unmarshal(reply.BuilderJSON, &builder); err != nil {
			return fmt.Errorf("decoding resolved builder: %w", err)
		}
		res, err := BuildBootstrapVM(&vmSpec, reply.OutputDir, reply.VmStateDir, reply.ExistingState, &distro, &builder, reply.BuilderImageRef)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote %s (rootfs sha256=%s)\n", res.DiskPath, res.BaseImageSHA256)
		if res.SeedIsoPath != "" {
			fmt.Fprintf(os.Stderr, "Wrote %s\n", res.SeedIsoPath)
		}
		builtDisk = res.DiskPath

	case "iso":
		var distro DistroDef
		if err := json.Unmarshal(reply.DistroJSON, &distro); err != nil {
			return fmt.Errorf("decoding resolved distro: %w", err)
		}
		res, err := BuildIsoVM(&vmSpec, reply.OutputDir, reply.VmStateDir, reply.ExistingState, &distro, reply.Force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote %s (blank — the installer partitions it)\n", res.DiskPath)
		fmt.Fprintf(os.Stderr, "Installer %s (sha256=%s)\n", res.InstallerIsoRef, res.InstallerSHA256)
		fmt.Fprintf(os.Stderr, "Wrote %s (answers: %s)\n", res.SeedIsoPath, strings.Join(res.SeedFiles, ", "))
		builtDisk = res.DiskPath

	case "container_disk":
		res, err := BuildContainerDisk(&vmSpec, reply.OutputDir, reply.VmStateDir, reply.ExistingState, reply.Force)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Pulled %s (sha256=%s)\n", vmSpec.Source.Image, res.BaseImageSHA256)
		fmt.Fprintf(os.Stderr, "Wrote %s\n", res.DiskPath)
		fmt.Fprintf(os.Stderr, "Wrote %s\n", res.SeedIsoPath)
		fmt.Fprintf(os.Stderr, "Instance-id: %s\n", res.InstanceID)
		builtDisk = res.DiskPath

	case "clone":
		// The unified from: name:tag drive: build the entity as a CLONE of ITSELF at
		// the deploy's snapshot (req.FromSnapshot — resolveVmBuild set reply.SourceKind
		// to "clone", the DRIVE, when the request carries from_snapshot). BuildClone
		// requires source.kind == clone + FromVm + FromSnapshot; set them from the drive.
		entity := entityLeaf(box)
		applyCloneDriveSource(&vmSpec, entity, req.FromSnapshot)
		if err := BuildClone(entity, &vmSpec, reply.OutputDir, reply.VmStateDir); err != nil {
			return err
		}
		cloneDir, derr := vmDiskDir(entity)
		if derr != nil {
			return derr
		}
		fmt.Fprintf(os.Stderr, "Wrote %s (clone of %s@%s)\n",
			filepath.Join(cloneDir, "disk.qcow2"), entity, req.FromSnapshot)
		if vmSpec.CloudInit != nil || vmSpec.SSH != nil {
			fmt.Fprintf(os.Stderr, "Wrote %s\n", filepath.Join(cloneDir, "seed.iso"))
		}
		builtDisk = filepath.Join(cloneDir, "disk.qcow2")

	default:
		return fmt.Errorf("vm %q: unsupported source.kind %q (want one of %s)", box, reply.SourceKind, strings.Join(knownVmSourceKinds, ", "))
	}

	// Box emission (cutover plan task 3 / WS-6.3): after EVERY successful
	// source-kind build, wrap the materialized disk + the entity's metadata into
	// a VM box image on local engine storage, tagged with the current CalVer.
	//
	// Default: BEST-EFFORT by contract — the disk build is the primary artifact
	// and the box is its metadata wrapper — so a missing engine or a failed emit
	// only warns and the build stays green. An explicit `--push` makes BOTH the
	// emit and the push load-bearing: the operator asked for a deliverable image,
	// so a failure is returned rather than silently skipped.
	vmName, _ := parseImageArg(box)
	ref, emitErr := emitVmBox(reply.Engine, vmName, &vmSpec, builtDisk, boxInImagePath(emit.ContainerDisk))
	if emit.Push != "" {
		if emitErr != nil {
			return fmt.Errorf("box emission for --push: %w", emitErr)
		}
		if err := pushVmBox(reply.Engine, ref, emit.Push); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Wrote VM box %s\n", ref)
		fmt.Fprintf(os.Stderr, "Pushed VM box %s\n", emit.Push)
		return nil
	}
	if emitErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: VM box emission skipped for %q (the disk build succeeded): %v\n", box, emitErr)
	} else {
		fmt.Fprintf(os.Stderr, "Wrote VM box %s\n", ref)
	}
	pruneAfterVmBuild()
	return nil
}

// pruneAfterVmBuild runs the SAME post-build retention prune plugin-box's box build runs
// (verb:retention, BuildPrune scope: per-run CalVer tag retention + stale .build staging
// dirs), so `charly vm build` does not leak its emitted box tags. Without it, the
// `localhost/<box>:<CalVer>` tags grow unbounded (measured: 24 tags of one bed box;
// opencharly/charly#808). Best-effort, warn-only — the disk build is the primary artifact,
// the box is its metadata wrapper. Mirrors candy/plugin-box's pruneAfterBuild (R3: the ONE
// verb:retention engine, reached the SAME peer-dispatch way).
func pruneAfterVmBuild() {
	if cmdExec == nil {
		return // no reverse channel (out-of-process placement) — nothing to prune through
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	keep, _ := loaderkit.ResolveRetentionDefaultsViaExecutor(cmdCtx, cmdExec, dir)
	reqJSON, jerr := vmBoxPruneRequestJSON(dir, keep)
	if jerr != nil {
		fmt.Fprintf(os.Stderr, "Warning: VM box retention prune: %v\n", jerr)
		return
	}
	resJSON, ierr := cmdExec.InvokeProvider(cmdCtx, "verb", "retention", sdk.OpRun, reqJSON, nil, sdk.InvokeProviderOpts{})
	if ierr != nil {
		fmt.Fprintf(os.Stderr, "Warning: VM box retention prune: %v\n", ierr)
		return
	}
	var reply spec.RetentionReply
	if len(resJSON) > 0 {
		if uerr := json.Unmarshal(resJSON, &reply); uerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: VM box retention prune: %v\n", uerr)
			return
		}
	}
	if len(reply.ImageRefs) > 0 {
		fmt.Fprintf(os.Stderr, "Pruned %d old VM box tag(s) (keep_images=%d)\n", len(reply.ImageRefs), keep)
	}
}

// vmBoxPruneRequestJSON is the PURE half of pruneAfterVmBuild — the retention request
// it sends (BuildPrune scope, the resolved keep_images, the project dir). Split out so
// the request the prune actually issues is unit-testable with no reverse channel.
func vmBoxPruneRequestJSON(dir string, keep int) ([]byte, error) {
	return json.Marshal(spec.RetentionRequest{Dir: dir, BuildPrune: true, KeepImages: keep})
}
