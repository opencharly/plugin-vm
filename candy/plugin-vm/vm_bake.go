package vm

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	if err := requireCloneSource(vmSpec, c.Box); err != nil {
		return err
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

	// Phase 2.5 — ensure qemu-guest-agent is ENABLED in the guest: the baked
	// disk's guest may not auto-start the agent service, and the re-materialized
	// clone disk wipes any prior in-guest enable. SSH in (the create path
	// published the managed alias) and enable it, then let it connect.
	if err := enableGuestAgent(c.Box, 8*time.Minute); err != nil {
		return fmt.Errorf("vm bake: enabling guest agent: %w", err)
	}

	// Phase 3.5 — wait for the guest agent to CONNECT before the strict
	// snapshot freeze (createConsistentSnapshot REQUIRES qemu-guest-agent
	// reachable; the domain was just booted so the agent needs time to come up).
	if err := waitForAgentConnect(c.Box, 3*time.Minute); err != nil {
		return fmt.Errorf("vm bake: guest agent not reachable before freeze: %w", err)
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

// enableGuestAgent SSHs into the freshly-booted guest and enables + starts
// qemu-guest-agent (the strict snapshot freeze requires it reachable; the
// baked disk's guest may ship the package without the service enabled at
// boot). Polls until the ssh command succeeds or the timeout expires.
//
// Constructed as a SUBPROCESS (exec.Command), NOT via VmSshCmd: that command
// leaf uses syscall.Exec (it replaces the process), which would terminate the
// whole bake on the first cold-boot ssh reset.
func enableGuestAgent(vmName string, timeout time.Duration) error {
	alias := "charly-" + vmName
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		args := []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			alias,
			"sudo", "systemctl", "enable", "--now", "qemu-guest-agent",
		}
		cmd := exec.Command("ssh", args...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out enabling qemu-guest-agent in %q", vmName)
	}
	return lastErr
}

// waitForAgentConnect polls the guest agent's ping until it answers or the
// timeout expires. The strict snapshot freeze requires a reachable
// qemu-guest-agent; the domain was just booted by phase 2, so the agent needs
// a bounded window to come up (the deploy path waits for SSH/cloud-init the
// same way).
func waitForAgentConnect(vmName string, timeout time.Duration) error {
	uri := readVmBackendURI()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, cerr := connectLibvirt(uri)
		if cerr == nil {
			dom, lerr := conn.lookupDomain("charly-" + vmName)
			if lerr == nil {
				agent := NewGuestAgent(conn.l, dom, 10*time.Second)
				if perr := agent.Ping(); perr == nil {
					_ = conn.Close()
					return nil
				} else {
					lastErr = perr
				}
			} else {
				lastErr = lerr
			}
			_ = conn.Close()
		} else {
			lastErr = cerr
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out waiting for the guest agent")
	}
	return lastErr
}

// requireCloneSource is the bake's source-kind guard: a layered VM bakes a
// clone base, so any other source kind is a hard error. Extracted for the
// unit test (the guard must fail a real non-clone spec, not a trivially-true
// comparison).
func requireCloneSource(vmSpec *VmSpec, vmName string) error {
	if vmSpec == nil {
		return noVmEntityErr(vmName)
	}
	if vmSpec.Source.Kind != "clone" {
		return fmt.Errorf("vm bake: source.kind must be clone (a layered VM bakes a clone base); entity %q has kind %q", vmName, vmSpec.Source.Kind)
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
