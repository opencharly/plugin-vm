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
	Create  VmSnapshotCreateCmd  `cmd:"" help:"Create a snapshot of a VM (external by default; internal with --mode internal)"`
	List    VmSnapshotListCmd    `cmd:"" help:"List snapshots for a VM"`
	Delete  VmSnapshotDeleteCmd  `cmd:"" help:"Delete a snapshot (refuses while clones/ephemerals reference it)"`
	Revert  VmSnapshotRevertCmd  `cmd:"" help:"Revert a VM to a snapshot"`
	Promote VmSnapshotPromoteCmd `cmd:"" help:"Convert an internal snapshot to external mode (extracts via qemu-img convert)"`
}

// VmSnapshotCreateCmd implements `charly vm snapshot create <vm> <name>`.
type VmSnapshotCreateCmd struct {
	Vm          string `arg:"" help:"VM name (kind:vm entity)"`
	Name        string `arg:"" help:"Snapshot name"`
	Mode        string `name:"mode" enum:"external,internal" default:"external" help:"Snapshot mode: external (clone-friendly, separate file) or internal (disk-efficient, embedded)"`
	Description string `name:"description" help:"Human-facing description of the snapshot"`
	Quiesce     bool   `name:"quiesce" help:"Flush guest state via guest-agent fsfreeze before snapshotting (falls back to libvirt's plain freeze)"`
	Domain      string `name:"domain" help:"Per-deploy domain identity (snapshots charly-<domain>, keyed by the DEPLOY not the entity); absent for a direct snapshot (domain = entity). A check bed's domain is the BED name."`
}

// Run executes `charly vm snapshot create`.
func (c *VmSnapshotCreateCmd) Run() error {
	entry, err := CreateSnapshot(SnapshotCreateOpts{
		VmName:      domainOr(c.Vm, c.Domain),
		SnapName:    c.Name,
		Mode:        c.Mode,
		Description: c.Description,
		Quiesce:     c.Quiesce,
	})
	if err != nil {
		return err
	}
	fmt.Printf("created %s snapshot %q on vm %q\n", entry.Mode, entry.Name, domainOr(c.Vm, c.Domain))
	if entry.DiskPath != "" {
		fmt.Printf("  disk: %s\n", entry.DiskPath)
	}
	return nil
}

// VmSnapshotListCmd implements `charly vm snapshot list <vm>`.
type VmSnapshotListCmd struct {
	Vm     string `arg:"" help:"VM name"`
	JSON   bool   `name:"json" help:"Emit JSON instead of a table"`
	Domain string `name:"domain" help:"Per-deploy domain identity (snapshots charly-<domain>, keyed by the DEPLOY not the entity); absent for a direct snapshot (domain = entity). A check bed's domain is the BED name."`
}

func (c *VmSnapshotListCmd) Run() error {
	entries, err := ListSnapshots(domainOr(c.Vm, c.Domain))
	if err != nil {
		return err
	}
	if c.JSON {
		return writeJSON(os.Stdout, entries)
	}
	if len(entries) == 0 {
		fmt.Printf("vm %q: no snapshots\n", domainOr(c.Vm, c.Domain))
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
	Domain string `name:"domain" help:"Per-deploy domain identity (snapshots charly-<domain>, keyed by the DEPLOY not the entity); absent for a direct snapshot (domain = entity). A check bed's domain is the BED name."`
}

func (c *VmSnapshotDeleteCmd) Run() error {
	if err := DeleteSnapshot(SnapshotDeleteOpts{
		VmName:   domainOr(c.Vm, c.Domain),
		SnapName: c.Name,
		Force:    c.Force,
	}); err != nil {
		return err
	}
	fmt.Printf("deleted snapshot %q on vm %q\n", c.Name, domainOr(c.Vm, c.Domain))
	return nil
}

// VmSnapshotRevertCmd implements `charly vm snapshot revert <vm> <name>`.
type VmSnapshotRevertCmd struct {
	Vm     string `arg:"" help:"VM name"`
	Name   string `arg:"" help:"Snapshot name"`
	Domain string `name:"domain" help:"Per-deploy domain identity (snapshots charly-<domain>, keyed by the DEPLOY not the entity); absent for a direct snapshot (domain = entity). A check bed's domain is the BED name."`
}

func (c *VmSnapshotRevertCmd) Run() error {
	if err := RevertSnapshot(domainOr(c.Vm, c.Domain), c.Name); err != nil {
		return err
	}
	fmt.Printf("reverted vm %q to snapshot %q\n", domainOr(c.Vm, c.Domain), c.Name)
	return nil
}

// VmSnapshotPromoteCmd implements `charly vm snapshot promote <vm> <name>`.
type VmSnapshotPromoteCmd struct {
	Vm     string `arg:"" help:"VM name"`
	Name   string `arg:"" help:"Snapshot name (must be mode=internal)"`
	Domain string `name:"domain" help:"Per-deploy domain identity (snapshots charly-<domain>, keyed by the DEPLOY not the entity); absent for a direct snapshot (domain = entity). A check bed's domain is the BED name."`
}

func (c *VmSnapshotPromoteCmd) Run() error {
	entry, err := PromoteSnapshot(domainOr(c.Vm, c.Domain), c.Name)
	if err != nil {
		return err
	}
	fmt.Printf("promoted snapshot %q on vm %q (now mode=external)\n", c.Name, domainOr(c.Vm, c.Domain))
	if entry.DiskPath != "" {
		fmt.Printf("  disk: %s\n", entry.DiskPath)
	}
	return nil
}
