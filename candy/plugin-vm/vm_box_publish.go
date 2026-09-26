package vm

// vm_box_publish.go — `charly vm box publish`: turn a VM's disk (the live disk,
// or a captured snapshot's disk) into a bootable containerDisk OCI image and
// push it to a registry.
//
// The emitter is verb:oci's container-disk-emit (candy/plugin-oci, where the
// go-containerregistry stack is single-homed): it streams the disk into a single
// application/vnd.oci.image.layer.v1.tar+gzip layer at the KubeVirt/Cua in-image
// path (deploykit.ContainerDiskPath = /disk/disk.img) and remote.Write's it. The
// in-image path contract and the VM-box metadata labels are the SDK's
// (deploykit.ContainerDiskPath + buildVmBoxMetadata), so this command adds no
// second source for either.
//
// This is the produce half of the containerDisk support the `container_disk`
// #VmSource arm consumes; it is reached over the F10 peer-dispatch leg
// (InvokeProvider "verb"/"oci") exactly as plugin-box reaches verb:oci for its
// merge and plugin-cache reaches it for its transport.

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

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
	LayoutDir    string `name:"layout-dir" help:"Also write the image as a local OCI Image Layout at this directory (consumable as oci:<dir>)"`
	Insecure     bool   `name:"insecure" help:"Allow a plain-HTTP (localhost dev) registry"`
}

// containerDiskEmitRequest mirrors candy/plugin-oci's container-disk-emit wire
// envelope. It is a JSON contract, decoded by the plugin-oci leg; the emit
// request type is not in spec yet (spec main carries the C7 checkstep change the
// released sdk cannot compile against), so the caller mirrors the envelope the
// same way pre-#149 cache callers did.
type containerDiskEmitRequest struct {
	DiskPath    string            `json:"disk_path"`
	InImagePath string            `json:"in_image_path,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Ref         string            `json:"ref"`
	Insecure    bool              `json:"insecure,omitempty"`
	LayoutDir   string            `json:"layout_dir,omitempty"`
}

// containerDiskEmitReply is the emit result the plugin-oci leg returns.
type containerDiskEmitReply struct {
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	LayerSize int64  `json:"layer_size"`
	LayoutDir string `json:"layout_dir,omitempty"`
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
	reply, err := publishContainerDisk(c.Box, vmSpec, disk, c.To, c.InImagePath, c.LayoutDir, c.Insecure)
	if err != nil {
		return err
	}
	fmt.Printf("published containerDisk %s@%s\n", reply.Ref, reply.Digest)
	fmt.Printf("  disk:       %s\n", disk)
	fmt.Printf("  media type: %s\n", reply.MediaType)
	fmt.Printf("  layer:      %d bytes\n", reply.LayerSize)
	if reply.LayoutDir != "" {
		fmt.Printf("  layout:     %s\n", reply.LayoutDir)
	}
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

// publishContainerDisk builds the containerDisk metadata labels and reaches
// verb:oci's container-disk-emit over the reverse channel. The labels carry the
// SAME spec.VmBoxMetadata contract the local VM box does, so a consumer can read
// either artifact through deploykit.VmCapabilitiesFromLabels.
func publishContainerDisk(vmName string, vmSpec *VmSpec, diskPath, ref, inImagePath, layoutDir string, insecure bool) (containerDiskEmitReply, error) {
	if cmdExec == nil {
		return containerDiskEmitReply{}, fmt.Errorf("vm box publish: no host reverse channel (command not compiled-in?)")
	}
	params, envJSON, err := containerDiskEmitEnvelope(vmName, vmSpec, diskPath, ref, inImagePath, layoutDir, insecure)
	if err != nil {
		return containerDiskEmitReply{}, err
	}
	out, err := cmdExec.InvokeProvider(cmdCtx, "verb", "oci", sdk.OpRun, params, envJSON, sdk.InvokeProviderOpts{})
	if err != nil {
		return containerDiskEmitReply{}, fmt.Errorf("vm box publish: %w", err)
	}
	var reply containerDiskEmitReply
	if len(out) > 0 {
		if uerr := json.Unmarshal(out, &reply); uerr != nil {
			return containerDiskEmitReply{}, fmt.Errorf("vm box publish: decode reply: %w", uerr)
		}
	}
	if err := validateContainerDiskReply(reply); err != nil {
		return containerDiskEmitReply{}, err
	}
	return reply, nil
}

// containerDiskEmitEnvelope is the PURE half of publishContainerDisk: it builds
// the container-disk-emit params (with the metadata labels and the defaulted
// in-image path) and the oci_op env selector, so the wire contract is
// unit-testable with no reverse channel.
func containerDiskEmitEnvelope(vmName string, vmSpec *VmSpec, diskPath, ref, inImagePath, layoutDir string, insecure bool) ([]byte, []byte, error) {
	if inImagePath == "" {
		inImagePath = deploykit.ContainerDiskPath
	}
	meta := buildVmBoxMetadata(vmName, vmSpec)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, nil, fmt.Errorf("vm box publish: marshaling metadata: %w", err)
	}
	labels := map[string]string{spec.LabelVmBox: string(metaJSON)}
	if meta.Version != "" {
		labels[spec.LabelVersion] = meta.Version
	}
	if meta.Description != "" {
		if descJSON, derr := json.Marshal(meta.Description); derr == nil {
			labels[spec.LabelDescription] = string(descJSON)
		}
	}
	params, err := json.Marshal(containerDiskEmitRequest{
		DiskPath:    diskPath,
		InImagePath: inImagePath,
		Labels:      labels,
		Ref:         ref,
		Insecure:    insecure,
		LayoutDir:   layoutDir,
	})
	if err != nil {
		return nil, nil, err
	}
	envJSON, err := json.Marshal(map[string]string{"oci_op": "container-disk-emit"})
	if err != nil {
		return nil, nil, err
	}
	return params, envJSON, nil
}

// validateContainerDiskReply is the PURE result guard: a successful emit must
// report a digest, and the layer must be the +gzip form Fleet/KubeVirt require
// (a green ref over an uncompressed layer would be a contract regression).
func validateContainerDiskReply(reply containerDiskEmitReply) error {
	if reply.Digest == "" {
		return fmt.Errorf("vm box publish: verb:oci returned no digest (the emit did not complete)")
	}
	if reply.MediaType != "application/vnd.oci.image.layer.v1.tar+gzip" {
		return fmt.Errorf("vm box publish: verb:oci emitted layer media type %q (want application/vnd.oci.image.layer.v1.tar+gzip)", reply.MediaType)
	}
	return nil
}
