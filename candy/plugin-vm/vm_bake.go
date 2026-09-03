package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/vmshared"
)

// vm_bake.go — the LAYERED VM bake (cutover task 6): the VM analog of the pod
// overlay Containerfile. A source.kind: clone entity is materialized (the
// base), the domain boots, the entity's own layers are applied IN-GUEST via the
// SHARED InstallPlan IR (the same walk the vm deploy runs — the layer
// application is the existing charly fleet add vm:<name> path, which the runner
// invokes between boot and the snapshot freeze), a consistent snapshot captures
// the baked state, and the box image wraps it.

// VmBakeCmd implements charly vm bake <name> [--candy a,b].
type VmBakeCmd struct {
	Box     string `arg:"" help:"VM name (kind:vm entity with source.kind: clone)"`
	Candy   string `name:"candy" help:"Comma-separated layers to apply in-guest BEFORE the snapshot freeze (delegated to charly fleet add vm:<name>)"`
	Console bool   `name:"console" help:"Enable console output for debugging the boot"`
}

// Run executes charly vm bake.
func (c *VmBakeCmd) Run() error {
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
	if vmSpec.Source.Kind != "clone" {
		return fmt.Errorf("vm bake: source.kind must be clone (a layered VM bakes a clone base); entity %q has kind %q", c.Box, vmSpec.Source.Kind)
	}

	rt, err := kit.ResolveRuntime()
	if err != nil {
		return err
	}
	engine := kit.EngineBinary(rt.RunEngine)
	vmStateDir, err := vmsharedStateDir(c.Box)
	if err != nil {
		return err
	}

	// Phase 1 — materialize the base (the clone overlay on the parent snapshot).
	fmt.Fprintf(os.Stderr, "bake %q: phase 1 — materializing the base (clone)\n", c.Box)
	if err := BuildClone(c.Box, vmSpec, "", vmStateDir); err != nil {
		return fmt.Errorf("vm bake: materializing base: %w", err)
	}

	// Phase 2 — boot the domain via the standard create path.
	fmt.Fprintf(os.Stderr, "bake %q: phase 2 — booting the domain\n", c.Box)
	createCmd := VmCreateCmd{Box: c.Box}
	if err := createCmd.Run(); err != nil {
		return fmt.Errorf("vm bake: booting %q: %w", c.Box, err)
	}

	// Phase 3 — the in-guest layer application IS the vm deploy's shared-IR
	// walk (charly fleet add vm:<name> runs kit.WalkPlans over the guest SSH
	// executor). Applied BEFORE the snapshot freeze so the baked box carries
	// them. With --candy, print the exact command and return (the runner
	// applies the layers, then re-runs WITHOUT --candy to freeze + emit).
	layers := splitCsv(c.Candy)
	if len(layers) > 0 {
		fmt.Fprintf(os.Stderr, "bake %q: phase 3 — apply the layer(s) in-guest with the shared IR walk:\n", c.Box)
		fmt.Fprintf(os.Stderr, "  charly fleet add vm:%s %s\n", c.Box, strings.Join(layers, " "))
		fmt.Fprintf(os.Stderr, "then re-run: charly vm bake %s (WITHOUT --candy) to freeze the baked state and emit the box\n", c.Box)
		return nil
	}

	// Phase 4 — freeze the baked state as a consistent snapshot.
	fmt.Fprintf(os.Stderr, "bake %q: phase 4 — freezing the baked state (snapshot)\n", c.Box)
	entry, err := createConsistentSnapshot(consistentCreateOpts(c.Box, "baked", "external", "layered bake of "+c.Box))
	if err != nil {
		return fmt.Errorf("vm bake: snapshot freeze: %w", err)
	}

	// Phase 5 — wrap the frozen disk into the box image.
	fmt.Fprintf(os.Stderr, "bake %q: phase 5 — emitting the VM box\n", c.Box)
	ref, err := emitVmBox(engine, c.Box, vmSpec, entry.DiskPath)
	if err != nil {
		return fmt.Errorf("vm bake: emitting box: %w", err)
	}
	fmt.Printf("baked VM box %q (snapshot %q, disk %s)\n", ref, entry.Name, entry.DiskPath)

	// Cleanup — stop the domain (the box is the artifact; the domain was the
	// bake vessel).
	stopCmd := VmStopCmd{Box: c.Box}
	if err := stopCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "vm bake: note: stopping the bake domain: %v\n", err)
	}
	return nil
}

// splitCsv splits a comma-separated layer list, trimming whitespace.
func splitCsv(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// vmsharedStateDir resolves the per-VM state dir for the entity.
func vmsharedStateDir(vmName string) (string, error) {
	base, err := vmshared.VmStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "charly-"+vmName), nil
}
