package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/opencharly/sdk/vmshared"
	"gopkg.in/yaml.v3"
)

// vm_clone.go — clone-from-snapshot build path.
//
// Two entry points:
//
//   - BuildClone(spec) — invoked by `charly vm build` when source.kind ==
//     "clone". Materializes a fresh per-VM qcow2 with the source
//     snapshot's external disk as backing chain. Regenerates the
//     cloud-init seed ISO with a fresh InstanceID (cloud-init MUST see
//     a new instance-id, otherwise it skips first-boot tasks). When
//     the clone declaration carries cloud_init_clean: true, the user-
//     data also injects `runcmd: cloud-init clean --machine-id --logs`
//     so the guest re-runs identity setup on first boot.
//
//   - writeVmCloneDeclaration — invoked by `charly vm clone` to persist a
//     kind:vm entry into charly.yml. Pure config-file
//     mutation; no disk operations.

// snapshotBackingStale walks a snapshot's qcow2 backing chain and reports the
// first backing file whose mtime is NEWER than the snapshot's capture time
// (or "" when the chain is fresh). A snapshot whose backing base was rebuilt
// after capture is stale: the guest filesystem (e.g. btrfs) inside the snapshot
// references inodes in the OLD base data, so a clone boots into grub rescue
// ("inode not found") instead of the guest. The check walks the FULL chain
// (qemu-img info --backing-chain), because the staleness is transitive — the
// snapshot's direct backing may be untouched while ITS backing (the leaf base)
// was rebuilt. -U opens read-only so a snapshot in use by a running clone is
// still inspectable. A qemu-img failure or a missing backing file is NOT
// treated as stale (a broken chain is a different, louder error at overlay
// create); only a genuinely newer backing file trips the guard.
func snapshotBackingStale(entry *vmshared.SnapshotEntry) (string, error) {
	if entry == nil || entry.DiskPath == "" || entry.Created == "" {
		return "", nil // nothing to check
	}
	created, err := time.Parse(time.RFC3339, entry.Created)
	if err != nil {
		return "", fmt.Errorf("parsing snapshot created time %q: %w", entry.Created, err)
	}
	cmd := exec.Command("qemu-img", "info", "--backing-chain", "-U", "--output", "json", entry.DiskPath)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("qemu-img info --backing-chain %s: %w", entry.DiskPath, err)
	}
	var chain []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(out, &chain); err != nil {
		return "", fmt.Errorf("parsing qemu-img backing chain: %w", err)
	}
	for _, img := range chain {
		if img.Filename == "" || img.Filename == entry.DiskPath {
			continue // the snapshot's own disk is not a backing file
		}
		fi, err := os.Stat(img.Filename)
		if err != nil {
			continue // a missing backing file is a different error (overlay create will fail loudly)
		}
		// The registry stores Created at RFC3339 second precision; the disk's
		// capture-finalization write can land sub-second after that timestamp.
		// Truncate the mtime to seconds so a same-second write is not a false
		// STALE (the snapshot is valid; only a genuinely later rebuild is stale).
		if fi.ModTime().Truncate(time.Second).After(created) {
			return img.Filename, nil
		}
	}
	return "", nil
}

// BuildClone is the source.kind == "clone" build path.
//
// vmName is the new VM (the clone target). spec is its VmSpec
// (source.from_vm and source.from_snapshot fully populated). outputDir
// is where output/qcow2/disk.qcow2 + output/qcow2/seed.iso will be
// written, mirroring the cloud_image build path's conventions.
func BuildClone(vmName string, spec *VmSpec, _, vmStateDir string) error {
	if spec.Source.Kind != "clone" {
		return fmt.Errorf("BuildClone called with source.kind == %q (want clone)", spec.Source.Kind)
	}
	if spec.Source.FromVm == "" {
		return fmt.Errorf("vm %q: source.from_vm is required for clone", vmName)
	}
	if spec.Source.FromSnapshot == "" {
		return fmt.Errorf("vm %q: source.from_snapshot is required for clone", vmName)
	}

	// Look up the source snapshot. Refuses if not found; auto-promotes
	// internal-mode snapshots to external before cloning.
	parentEntry, err := LookupSnapshot(spec.Source.FromVm, spec.Source.FromSnapshot)
	if err != nil {
		return err
	}
	if parentEntry.Mode == "internal" {
		fmt.Fprintf(os.Stderr, "note: snapshot %s@%s is mode=internal; auto-promoting to external for clone backing\n",
			spec.Source.FromVm, spec.Source.FromSnapshot)
		parentEntry, err = PromoteSnapshot(spec.Source.FromVm, spec.Source.FromSnapshot)
		if err != nil {
			return fmt.Errorf("auto-promoting %s@%s: %w", spec.Source.FromVm, spec.Source.FromSnapshot, err)
		}
	}
	if parentEntry.DiskPath == "" {
		return fmt.Errorf("vm %q: parent snapshot %s@%s has no disk path", vmName, spec.Source.FromVm, spec.Source.FromSnapshot)
	}

	// STALE-SNAPSHOT GUARD (R1, the grub-rescue regression): a snapshot whose
	// backing chain was rebuilt AFTER the snapshot was captured is stale — the
	// guest filesystem (e.g. btrfs) inside the snapshot references inodes in
	// the OLD base data, so a clone boots into grub rescue ("inode not found")
	// instead of the guest. Detecting this at clone time turns that silent
	// boot failure into a loud, actionable error naming the stale backing file
	// and the refresh path (re-run the base bed that captured the snapshot).
	if stale, serr := snapshotBackingStale(parentEntry); serr != nil {
		return fmt.Errorf("vm %q: checking snapshot %s@%s freshness: %w", vmName, spec.Source.FromVm, spec.Source.FromSnapshot, serr)
	} else if stale != "" {
		return fmt.Errorf("vm %q: snapshot %s@%s is STALE: its backing disk %s was modified after the snapshot was captured (%s). The guest filesystem inside the snapshot references the OLD base data, so a clone would fail to boot. Re-run the base bed that captured the snapshot (e.g. check-vm-clone-base) to refresh it before cloning",
			vmName, spec.Source.FromVm, spec.Source.FromSnapshot, stale, parentEntry.Created)
	}

	// Materialize the clone overlay using the existing primitive.
	clonePath := filepath.Join(vmDiskDir(vmName), "disk.qcow2")
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	if err := qemuImgCreateOverlay(parentEntry.DiskPath, clonePath); err != nil {
		return fmt.Errorf("clone overlay create: %w", err)
	}

	// Increment the parent snapshot's refcount. The decrement happens
	// at ephemeral / clone teardown.
	if err := IncrementSnapshotRefcount(spec.Source.FromVm, spec.Source.FromSnapshot); err != nil {
		// Don't fail the build if refcount bookkeeping is off-by-one;
		// log and proceed.
		fmt.Fprintf(os.Stderr, "warning: incrementing snapshot refcount for %s@%s: %v\n",
			spec.Source.FromVm, spec.Source.FromSnapshot, err)
	}

	// Regenerate the cloud-init seed ISO with a fresh InstanceID.
	// Pass nil for existingState — that's the path that auto-generates
	// a new UUIDv4 (see vm_cloud_image.go:164-169).
	seedPath := filepath.Join(vmDiskDir(vmName), "seed.iso")
	if spec.CloudInit != nil || spec.SSH != nil {
		// If cloud_init_clean is set, inject the clean runcmd so
		// machine-id and ssh host keys regenerate on first boot.
		if spec.Source.CloudInitClean {
			if spec.CloudInit == nil {
				spec.CloudInit = &VmCloudInit{}
			}
			spec.CloudInit.RunCmd = appendCloudInitClean(spec.CloudInit.RunCmd)
		}
		if err := RegenerateSeedISO(spec, seedPath, vmStateDir, nil); err != nil {
			return fmt.Errorf("regenerating seed ISO for clone: %w", err)
		}
	}
	return nil
}

// appendCloudInitClean adds the cloud-init clean runcmd entry to a
// runcmd list (idempotent — won't duplicate if the entry's already
// there).
func appendCloudInitClean(existing []string) []string {
	const cleanCmd = "cloud-init clean --machine-id --logs"
	if slices.Contains(existing, cleanCmd) {
		return existing
	}
	return append(existing, cleanCmd)
}

// findOrCreateMapEntry locates a top-level map key in a mapping node
// and returns its value mapping. If absent, appends a fresh empty
// mapping and returns it.
func findOrCreateMapEntry(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key && parent.Content[i+1].Kind == yaml.MappingNode {
			return parent.Content[i+1]
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valNode)
	return valNode
}

// alreadyHas reports whether a mapping node has the given key.
func alreadyHas(parent *yaml.Node, key string) bool {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return true
		}
	}
	return false
}

func addStrPair(parent *yaml.Node, key, val string) {
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val},
	)
}

func addBoolPair(parent *yaml.Node, key string, val bool) {
	v := "false"
	if val {
		v = "true"
	}
	parent.Content = append(parent.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v},
	)
}
