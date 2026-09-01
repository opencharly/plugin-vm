package vm

import (
	"fmt"
	"os"
	"text/tabwriter"
)

// vm_snapshot_cmd.go — Kong subcommand wiring for `charly vm snapshot {…}`.
// Wired into VmCmd via the Snapshot field (see vm.go).

// VmSnapshotCmd is the parent of `charly vm snapshot`.
type VmSnapshotCmd struct {
	Create           VmSnapshotCreateCmd           `cmd:"" help:"Create a snapshot of a VM (external by default; internal with --mode internal)"`
	CreateConsistent VmSnapshotCreateConsistentCmd `cmd:"" name:"create-consistent" help:"Create a guest-consistent snapshot (guest-agent fsfreeze -> snapshot -> thaw; strict, no silent fallback)"`
	List             VmSnapshotListCmd             `cmd:"" help:"List snapshots for a VM"`
	Delete           VmSnapshotDeleteCmd           `cmd:"" help:"Delete a snapshot (refuses while clones/ephemerals reference it)"`
	Revert           VmSnapshotRevertCmd           `cmd:"" help:"Revert a VM to a snapshot"`
	RevertAndStart   VmSnapshotRevertAndStartCmd   `cmd:"" name:"revert-and-start" help:"Revert a VM to a snapshot and start it (stops the VM first: revert requires the domain offline)"`
	Promote          VmSnapshotPromoteCmd          `cmd:"" help:"Convert an internal snapshot to external mode (extracts via qemu-img convert)"`
}

// snapshotVmName resolves the effective snapshot identity. The snapshot surface
// is keyed on the kind:vm ENTITY name (paths + registry + `charly-<name>` domain
// lookup). A check bed's live domain is named after the DEPLOY (charly-<BedDomain>,
// #33/P33), so the snapshot commands accept --domain <deploy-name> to target a
// bed's domain instead: the deploy name replaces the entity name for ALL snapshot
// purposes (registry, disk paths, domain lookup).
func snapshotVmName(entity, domain string) string {
	if domain != "" {
		return domain
	}
	return entity
}

// VmSnapshotCreateCmd implements `charly vm snapshot create <vm> <name>`.
type VmSnapshotCreateCmd struct {
	Vm          string `arg:"" help:"VM name (kind:vm entity)"`
	Name        string `arg:"" help:"Snapshot name"`
	Mode        string `name:"mode" enum:"external,internal" default:"external" help:"Snapshot mode: external (clone-friendly, separate file) or internal (disk-efficient, embedded)"`
	Description string `name:"description" help:"Human-facing description of the snapshot"`
	Quiesce     bool   `name:"quiesce" help:"Flush guest state via guest-agent fsfreeze before snapshotting (falls back to libvirt's plain freeze)"`
	Domain      string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

// Run executes `charly vm snapshot create`.
func (c *VmSnapshotCreateCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	entry, err := CreateSnapshot(SnapshotCreateOpts{
		VmName:      vmName,
		SnapName:    c.Name,
		Mode:        c.Mode,
		Description: c.Description,
		Quiesce:     c.Quiesce,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created %s snapshot %q on vm %q\n", entry.Mode, entry.Name, c.Vm)
	if entry.DiskPath != "" {
		fmt.Printf("  disk: %s\n", entry.DiskPath)
	}
	return nil
}

// VmSnapshotCreateConsistentCmd implements `charly vm snapshot create-consistent <vm> <name>`.
// The §5.3 composite verb: ONE step that produces a GUARANTEED-consistent snapshot —
// strict agent precondition -> quiesced create (atomic libvirt freeze+snapshot+thaw)
// — with no silent fallback to a non-quiesced snapshot (the distinction from
// `create --quiesce`, which retries without the flag when the agent is unavailable).
// Orchestration lives in vm_snapshot_composites.go (createConsistentSnapshot).
type VmSnapshotCreateConsistentCmd struct {
	Vm          string `arg:"" help:"VM name (kind:vm entity)"`
	Name        string `arg:"" help:"Snapshot name"`
	Mode        string `name:"mode" enum:"external,internal" default:"external" help:"Snapshot mode: external (clone-friendly, separate file) or internal (disk-efficient, embedded)"`
	Description string `name:"description" help:"Human-facing description of the snapshot"`
	Domain      string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

// Run executes `charly vm snapshot create-consistent`.
func (c *VmSnapshotCreateConsistentCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	entry, err := createConsistentSnapshot(consistentCreateOpts(vmName, c.Name, c.Mode, c.Description))
	if err != nil {
		return err
	}
	fmt.Printf("created consistent %s snapshot %q on vm %q\n", entry.Mode, entry.Name, c.Vm)
	if entry.DiskPath != "" {
		fmt.Printf("  disk: %s\n", entry.DiskPath)
	}
	return nil
}

// VmSnapshotListCmd implements `charly vm snapshot list <vm>`.
type VmSnapshotListCmd struct {
	Vm     string `arg:"" help:"VM name"`
	JSON   bool   `name:"json" help:"Emit JSON instead of a table"`
	Domain string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

func (c *VmSnapshotListCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	entries, err := ListSnapshots(vmName)
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(os.Stdout, entries)
	}
	if len(entries) == 0 {
		fmt.Printf("vm %q: no snapshots\n", c.Vm)
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tMODE\tCREATED\tREFCOUNT\tDESCRIPTION")
	for _, e := range entries {
		desc := e.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n", e.Name, e.Mode, e.Created, e.Refcount, desc)
	}
	return tw.Flush()
}

// VmSnapshotDeleteCmd implements `charly vm snapshot delete <vm> <name>`.
type VmSnapshotDeleteCmd struct {
	Vm     string `arg:"" help:"VM name"`
	Name   string `arg:"" help:"Snapshot name"`
	Force  bool   `name:"force" help:"Delete even when refcount > 0 (only safe after destroying consumers)"`
	Domain string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

func (c *VmSnapshotDeleteCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	if err := DeleteSnapshot(SnapshotDeleteOpts{
		VmName:   vmName,
		SnapName: c.Name,
		Force:    c.Force,
	}); err != nil {
		return err
	}
	fmt.Printf("deleted snapshot %q on vm %q\n", c.Name, c.Vm)
	return nil
}

// VmSnapshotRevertCmd implements `charly vm snapshot revert <vm> <name>`.
type VmSnapshotRevertCmd struct {
	Vm     string `arg:"" help:"VM name"`
	Name   string `arg:"" help:"Snapshot name"`
	Domain string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

func (c *VmSnapshotRevertCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	if err := RevertSnapshot(vmName, c.Name); err != nil {
		return err
	}
	fmt.Printf("reverted vm %q to snapshot %q\n", c.Vm, c.Name)
	return nil
}

// VmSnapshotRevertAndStartCmd implements `charly vm snapshot revert-and-start <vm> <name>`.
// The §5.3 composite verb: encapsulates the offline-domain revert semantics —
// stop the VM if running -> revert the snapshot -> start the VM. The stop is
// REQUIRED for the revert to be able to run at all (qemu-img refuses to mutate a
// live qcow2; libvirt's external revert also needs the domain offline).
// Orchestration lives in vm_snapshot_composites.go (revertAndStartVm).
type VmSnapshotRevertAndStartCmd struct {
	Vm     string `arg:"" help:"VM name (kind:vm entity)"`
	Name   string `arg:"" help:"Snapshot name"`
	Domain string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

func (c *VmSnapshotRevertAndStartCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	if err := revertAndStartVm(vmName, c.Name); err != nil {
		return err
	}
	fmt.Printf("reverted vm %q to snapshot %q and started it\n", c.Vm, c.Name)
	return nil
}

// VmSnapshotPromoteCmd implements `charly vm snapshot promote <vm> <name>`.
type VmSnapshotPromoteCmd struct {
	Vm     string `arg:"" help:"VM name"`
	Name   string `arg:"" help:"Snapshot name (must be mode=internal)"`
	Domain string `name:"domain" help:"Target a check bed's per-deploy domain (charly-<deploy>) instead of the entity's own domain"`
}

func (c *VmSnapshotPromoteCmd) Run() error {
	vmName := snapshotVmName(c.Vm, c.Domain)
	entry, err := PromoteSnapshot(vmName, c.Name)
	if err != nil {
		return err
	}
	fmt.Printf("promoted snapshot %q on vm %q (now mode=external)\n", c.Name, c.Vm)
	if entry.DiskPath != "" {
		fmt.Printf("  disk: %s\n", entry.DiskPath)
	}
	return nil
}
