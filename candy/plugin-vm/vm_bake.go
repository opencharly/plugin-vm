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
// application is the existing charly deploy add vm:<name> path, which the runner
// invokes between boot and the snapshot freeze), a consistent snapshot captures
// the baked state, and the box image wraps it.

// VmBakeCmd implements charly vm bake <name> [--candy a,b].
type VmBakeCmd struct {
	Box           string `arg:"" help:"VM name (the distro-bearing kind:vm entity whose golden snapshot bakes)"`
	Candy         string `name:"candy" help:"Comma-separated layers to apply in-guest BEFORE the snapshot freeze (delegated to charly deploy add vm:<name>)"`
	Console       bool   `name:"console" help:"Enable console output for debugging the boot"`
	FromSnapshot  string `name:"from-snapshot" help:"the golden snapshot to bake (required — the bake materializes the base as a clone of the entity's own golden at this snapshot)"`
	Domain        string `name:"domain" help:"Bake against a per-deploy DOMAIN (a disposable vm: deploy of this entity) instead of the entity's own identity. The golden snapshot is looked up on this domain; the bake boots + flattens the domain's own overlay. Use this from a bed/automation so the destroyed VM is the deploy (which must be disposable: true), never the shared entity."`
	ContainerDisk bool   `name:"container-disk" help:"Emit the baked box with the disk at /disk/disk.img (the KubeVirt containerDisk contract a cluster boots directly) instead of the default /disk.qcow2. This is the produce half for a Cua Fleet / KubeVirt image: the in-guest candy bake is frozen and delivered as a containerDisk."`
	Push          string `name:"push" help:"After emitting, retag and push the baked box image to this registry-pullable ref. An explicit --push makes the delivery load-bearing."`
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
	// The bake base is the entity's OWN golden: it marks the frozen base the bake boots
	// onto. It MUST exist — a known-good base is the whole point of requiring it.
	if err := bakeRequiresSnapshot(c.FromSnapshot); err != nil {
		return err
	}

	// The bake drives a DOMAIN IDENTITY. With --domain (a bed/automation path) the domain
	// IS the deploy the caller declared `disposable: true`; without it (the direct operator
	// path) the domain is a derived `<entity>-bake`. The domain keys EVERY subsequent verb
	// (create/stop/destroy) AND the snapshot registry (snapshotVmName), so the golden the
	// bake looks up is the one captured ON this domain.
	bakeDomain := bakeDomainName(c.Box, c.Domain)
	if _, err := LookupSnapshot(bakeDomain, c.FromSnapshot); err != nil {
		return err
	}

	rt, err := kit.ResolveRuntime()
	if err != nil {
		return err
	}
	engine := kit.EngineBinary(rt.RunEngine)

	// Phase 1/2 — boot the bake domain: its disk is the domain's own overlay onto the
	// frozen base, so the base — and the golden snapshot that backs onto it — stay
	// immutable. This REPLACES the former BuildClone, which materialized the clone INTO
	// the entity's own disk (the golden's backing file) and produced a circular qcow2
	// backing chain (plugin-vm#54: `qemu-img: Backing file … creates an infinite loop`).
	fmt.Fprintf(os.Stderr, "bake %q: phase 1/2 — booting the bake domain %q (per-domain overlay onto the frozen base)\n", c.Box, bakeDomain)
	if err := bakeCreateCmd(c.Box, c.Domain, bakeDomain).Run(); err != nil {
		return fmt.Errorf("vm bake: booting %q: %w", c.Box, err)
	}

	// Phase 2.5 — BEST-EFFORT: enable qemu-guest-agent in the guest (the deploy path's
	// consistent operations use it). It is NOT load-bearing for the bake — the bake does
	// not freeze a snapshot — and a guest whose desktop user has no NOPASSWD sudo (the Cua
	// Fleet image) cannot enable it over ssh. A failure is reported, never fatal.
	if err := enableGuestAgent(bakeDomain, 2*time.Minute); err != nil {
		fmt.Fprintf(os.Stderr, "vm bake: note: qemu-guest-agent not enabled (%v) — the bake does not require it\n", err)
	} else if err := waitForAgentConnect(bakeDomain, 2*time.Minute); err != nil {
		fmt.Fprintf(os.Stderr, "vm bake: note: qemu-guest-agent not reachable (%v) — the bake does not require it\n", err)
	}

	// Phase 3 — the in-guest layer application IS the vm deploy's shared-IR
	// walk (charly deploy add vm:<name> runs kit.WalkPlans over the guest SSH
	// executor). With --candy, print the exact command and return (the runner
	// applies the layers, then re-runs WITHOUT --candy to flatten + emit).
	layers := splitCsv(c.Candy)
	if len(layers) > 0 {
		fmt.Fprintf(os.Stderr, "bake %q: phase 3 — apply the layer(s) in-guest with the shared IR walk:\n", c.Box)
		fmt.Fprintf(os.Stderr, "  charly deploy add vm:%s --domain %s %s\n", c.Box, bakeDomain, strings.Join(layers, " "))
		fmt.Fprintf(os.Stderr, "then re-run: charly vm bake %s --from-snapshot %s --domain %s (WITHOUT --candy) to flatten the baked state and emit the box\n", c.Box, c.FromSnapshot, bakeDomain)
		return nil
	}

	// Phase 4 — stop the bake domain so its overlay is a consistent file, then FLATTEN
	// it into a standalone disk. The emitted box must carry the WHOLE baked disk; the
	// former code emitted a snapshot OVERLAY whose backing would be absent from the
	// image.
	fmt.Fprintf(os.Stderr, "bake %q: phase 4 — stopping the bake domain + flattening the baked disk\n", c.Box)
	if err := (&VmStopCmd{Box: c.Box, Domain: bakeDomain, Force: true}).Run(); err != nil {
		return fmt.Errorf("vm bake: stopping the bake domain: %w", err)
	}
	bakeDisk, err := bakeWorkingDisk(bakeDomain)
	if err != nil {
		return err
	}
	flatDisk := filepath.Join(filepath.Dir(bakeDisk), "baked-flat.qcow2")
	_ = os.Remove(flatDisk)
	if err := qemuImgConvert(bakeDisk, flatDisk); err != nil {
		return fmt.Errorf("vm bake: flattening the baked disk: %w", err)
	}

	// Phase 5 — wrap the flattened disk into the box image. --container-disk emits
	// the KubeVirt/Cua /disk/disk.img layout (boxInImagePath); an explicit
	// --push then delivers the baked box to a registry — the produce half for a
	// Cua Fleet / KubeVirt image (the in-guest candy bake IS the difference from
	// `vm build`).
	fmt.Fprintf(os.Stderr, "bake %q: phase 5 — emitting the VM box\n", c.Box)
	entry := &SnapshotEntry{Name: "baked", DiskPath: flatDisk}
	if err := runBakePhase5(engine, c.Box, vmSpec, entry, vmBoxEmitOpts{ContainerDisk: c.ContainerDisk, Push: c.Push}); err != nil {
		return err
	}

	// Cleanup — destroy the bake domain + its overlay (the box is the artifact).
	// --domain: the domain is a disposable deploy the caller owns (its destroy is the
	// deploy's own teardown; KeepDeploy stays true so a `vm:` deploy's charly.yml entry
	// is untouched). Without --domain: the derived `<entity>-bake` domain is a throwaway
	// vessel, not a deploy.
	if err := (&VmDestroyCmd{Box: c.Box, Domain: bakeDomain, Disk: true, KeepDeploy: true}).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "vm bake: note: destroying the bake domain: %v\n", err)
	}
	return nil
}

// bakeDomainName resolves the DOMAIN IDENTITY a bake drives. With an explicit --domain the
// domain IS the caller's deploy (which must be `disposable: true` so the bake's destroy is
// the deploy's own authorized teardown); otherwise it is the derived `<entity>-bake`. The
// domain keys EVERY bake verb (create/stop/destroy) AND the snapshot registry — the golden
// is looked up on THIS domain, never the entity's. Pure, so the wiring is unit-testable.
func bakeDomainName(entity, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return entity + "-bake"
}

// bakeCreateCmd assembles the bake's phase-1/2 create. The DOMAIN is always set (it IS the
// bake's identity); KeepDisk is set ONLY when the caller supplied an explicit --domain —
// that domain's disk is the one a golden captured on it backs onto, so the create must be
// non-destructive. The derived `<entity>-bake` path (no --domain) creates a fresh overlay
// as before. Pure, so the KeepDisk wiring is unit-testable (a regression drops the golden's
// backing on the deploy path).
func bakeCreateCmd(box, explicitDomain, bakeDomain string) *VmCreateCmd {
	return &VmCreateCmd{Box: box, Domain: bakeDomain, KeepDisk: explicitDomain != ""}
}

// bakeWorkingDisk is the bake domain's per-domain overlay — the writable working disk the
// bake boots and flattens. Keyed by the DOMAIN (charly-<domain>), matching the create
// path's per-domain state dir (runVmSpecCreate: filepath.Join(vmStateBase, "charly-"+domain)).
func bakeWorkingDisk(bakeDomain string) (string, error) {
	dir, err := vmsharedStateDir(bakeDomain)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "disk.qcow2"), nil
}

// qemuImgConvert flattens src into a standalone qcow2 at dst (no backing file), so an
// emitted box carries the whole disk rather than an overlay whose backing is absent.
func qemuImgConvert(src, dst string) error {
	cmd := exec.Command("qemu-img", "convert", "-O", "qcow2", src, dst)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img convert %s -> %s: %w", src, dst, err)
	}
	return nil
}

// The bake's emit/push seams. Package vars so phase 5's flag wiring is
// unit-testable without a live engine or registry (the engineCmd precedent);
// they default to the ONE emit (emitVmBox) and push (pushVmBox) primitives.
var (
	bakeEmitBox = emitVmBox
	bakePushBox = pushVmBox
)

// runBakePhase5 is phase 5 of `charly vm bake`: emit the frozen disk as a VM box
// and, with an explicit --push, deliver it. The in-image path comes from
// boxInImagePath(emit.ContainerDisk) — the SAME flag→path mapping `vm build`
// uses, so a baked box lands at the KubeVirt/Cua /disk/disk.img contract. The
// emit is load-bearing (a bake that cannot emit its box has not produced its
// artifact); --push additionally makes the delivery load-bearing.
func runBakePhase5(engine, box string, vmSpec *VmSpec, entry *SnapshotEntry, emit vmBoxEmitOpts) error {
	ref, emitErr := bakeEmitBox(engine, box, vmSpec, entry.DiskPath, boxInImagePath(emit.ContainerDisk))
	if emitErr != nil {
		return fmt.Errorf("vm bake: emitting box: %w", emitErr)
	}
	if emit.Push != "" {
		if err := bakePushBox(engine, ref, emit.Push); err != nil {
			return fmt.Errorf("vm bake: pushing box: %w", err)
		}
		fmt.Printf("baked VM box %q (snapshot %q, disk %s)\n", ref, entry.Name, entry.DiskPath)
		fmt.Printf("pushed VM box %s\n", emit.Push)
		return nil
	}
	fmt.Printf("baked VM box %q (snapshot %q, disk %s)\n", ref, entry.Name, entry.DiskPath)
	return nil
}

// enableGuestAgent SSHs into the freshly-booted guest and enables + starts
// qemu-guest-agent (the strict snapshot freeze requires it reachable; the
// baked disk's guest may ship the package without the service enabled at
// boot). Bounded poll — the guest may still be mid-cloud-init on the fresh
// clone disk, so ssh retries until it answers or the timeout expires.
//
// Constructed as a SUBPROCESS (exec.Command), NOT via VmSshCmd: that command
// leaf uses syscall.Exec (it replaces the process), which would terminate the
// whole bake on the first cold-boot ssh reset.
func enableGuestAgent(vmName string, timeout time.Duration) error {
	alias := "charly-" + vmName
	return bakePollUntil(func() error {
		cmd := exec.Command("ssh", guestAgentEnableSshArgs(alias)...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}, timeout, 5*time.Second)
}

// guestAgentEnableSshArgs is the exact ssh invocation that enables the guest
// agent — extracted from enableGuestAgent so the command construction is
// unit-testable (the subprocess itself needs a live guest).
func guestAgentEnableSshArgs(alias string) []string {
	return []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		alias,
		"sudo", "systemctl", "enable", "--now", "qemu-guest-agent",
	}
}

// pollUntil runs probe until it returns nil or the timeout expires. Bounded
// polling is the bake's wait primitive — used both for the ssh agent-enable
// and the libvirt agent-ping. Pure and testable with a fake probe.
func bakePollUntil(probe func() error, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := probe(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %v", timeout)
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
	return bakePollUntil(func() error {
		conn, cerr := connectLibvirt(uri)
		if cerr != nil {
			return cerr
		}
		defer conn.Close() //nolint:errcheck
		dom, lerr := conn.lookupDomain("charly-" + vmName)
		if lerr != nil {
			return lerr
		}
		agent := NewGuestAgent(conn.l, dom, 10*time.Second)
		return agent.Ping()
	}, timeout, 5*time.Second)
}

// The bake base clone-source wiring uses the SAME extracted seam as the build drive
// (applyCloneDriveSource — vm_build.go): source.kind = clone, from_vm = the entity
// itself, from_snapshot = the named golden (R3 — one wiring, two consumers).

// bakeRequiresSnapshot is the bake's guard: the base materializes as a clone of the
// entity's OWN golden, so the snapshot name is required (the retired entity clone arm
// used to carry it). Extracted for the unit test (the guard must fail a real empty
// flag, not a trivially-true comparison).
func bakeRequiresSnapshot(fromSnapshot string) error {
	if fromSnapshot == "" {
		return fmt.Errorf("vm bake: --from-snapshot <tag> is required (bake materializes the base as a clone of the entity's own golden at that snapshot)")
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
