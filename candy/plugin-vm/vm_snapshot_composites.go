package vm

import (
	"fmt"
	"os"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// vm_snapshot_composites.go — the §5.3 snapshot COMPOSITE verbs
// (`charly vm snapshot create-consistent` / `revert-and-start`). Each composite
// is ONE step made of existing primitives, orchestrated here so the ORDER
// contract is testable without a live libvirt/qemu (revertAndStartSteps seam)
// and so the strict-quiesce wiring (fsfreeze -> snapshot -> thaw) has one home.

// --- create-consistent -------------------------------------------------

// consistentCreateOpts builds the SnapshotCreateOpts for create-consistent.
// Quiesce is ALWAYS true at the command surface: the registry records the
// snapshot as quiesced, and libvirt's quiesce flag re-freezes an already-frozen
// filesystem as a no-op (modern qemu-guest-agent freeze is idempotent) and
// thaws after the snapshot. The composite (createConsistentSnapshot) clears it
// only for the paths where no guest agent can exist (stopped/absent domain,
// internal mode) — there the snapshot is consistent by virtue of being offline.
func consistentCreateOpts(vm, name, mode, desc string) SnapshotCreateOpts {
	opts := SnapshotCreateOpts{
		VmName:      vm,
		SnapName:    name,
		Mode:        mode,
		Description: desc,
		Quiesce:     true,
	}
	if opts.Mode == "" {
		opts.Mode = "external"
	}
	return opts
}

// createConsistentSnapshot implements the body of create-consistent: ONE step
// that produces a GUARANTEED-consistent snapshot. The strictness that separates
// this verb from `create --quiesce`: the guest agent must be reachable AND
// libvirt's quiesce flag must succeed — the command FAILS, it never silently
// falls back to a non-quiesced snapshot (create --quiesce retries without the
// flag; see vm_snapshot_libvirt.go createExternalSnapshot). libvirt's quiesce
// flag freezes + snapshots + thaws ATOMICALLY, so no manual freeze/thaw is
// needed and a mid-flight failure cannot leave the guest filesystems frozen.
//
// Mode-dependent semantics:
//   - external mode, domain RUNNING: agent Ping (strict) -> CreateSnapshot with
//     Quiesce=true under strictQuiesce (libvirt freezes, snapshots, thaws; the
//     registry records Quiesced=true). The composite must NOT pre-freeze: a
//     second freeze on an already-frozen filesystem is REJECTED by
//     qemu-guest-agent ("command has been disabled"), which would trip the
//     fallback and silently produce a non-quiesced snapshot.
//   - external mode, domain NOT running (or absent): no guest agent to talk to —
//     a stopped VM is inherently consistent, so the snapshot is created plain
//     (Quiesce=false; libvirt's quiesce flag would fail against a dead agent).
//   - internal mode: qemu-img cannot mutate a live qcow2, so a RUNNING domain is a
//     hard error — a consistent internal snapshot requires the VM stopped.
func createConsistentSnapshot(opts SnapshotCreateOpts) (entry *SnapshotEntry, retErr error) {
	if opts.VmName == "" {
		return nil, fmt.Errorf("create-consistent: vm name is required")
	}
	if opts.SnapName == "" {
		return nil, fmt.Errorf("create-consistent: snapshot name is required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = "external"
	}

	// Resolve the domain state once — the composite branches on it. A connect or
	// lookup failure falls through to the plain create path (which surfaces its
	// own error: external needs the libvirt domain, internal only qemu-img).
	uri := readVmBackendURI()
	conn, cerr := connectLibvirt(uri)
	var dom libvirt.Domain
	domFound := false
	domRunning := false
	if cerr == nil {
		defer conn.Close() //nolint:errcheck
		d, lerr := conn.lookupDomain("charly-" + opts.VmName)
		if lerr == nil {
			domFound = true
			dom = d
			if st, serr := conn.domainState(d); serr == nil {
				domRunning = st == libvirt.DomainRunning
			}
		}
	}

	if mode == "internal" && domFound && domRunning {
		return nil, fmt.Errorf("create-consistent: internal-mode consistent snapshot requires the VM stopped (qemu-img cannot mutate a live qcow2); stop the VM or use external mode")
	}

	if mode == "external" && domFound && domRunning {
		// Strict quiesce: the guest agent must be reachable, and libvirt's
		// quiesce flag must SUCCEED (strictQuiesce disables createExternalSnapshot's
		// best-effort fallback). libvirt's quiesce flag freezes + snapshots +
		// thaws ATOMICALLY — the composite must NOT pre-freeze itself: a second
		// freeze on an already-frozen filesystem is REJECTED by qemu-guest-agent
		// ("command has been disabled"), which would trip the fallback and silently
		// produce a non-quiesced snapshot while the registry records Quiesced=true.
		// A stopped VM skips this branch entirely (inherently consistent, no agent
		// to talk to).
		agent := NewGuestAgent(conn.l, dom, 10*time.Second)
		if err := agent.Ping(); err != nil {
			return nil, fmt.Errorf("create-consistent: qemu-guest-agent unreachable: %w (create-consistent REQUIRES qemu-guest-agent in the guest; use 'charly vm snapshot create --quiesce' for best-effort quiesce, or stop the VM for an inherently consistent snapshot)", err)
		}
		strictQuiesce.Store(true)
		defer strictQuiesce.Store(false)
		var err error
		entry, err = CreateSnapshot(opts) // opts.Quiesce is true here
		if err != nil {
			return nil, err
		}
		return entry, nil
	}

	// Stopped/absent domain (external) or internal mode: inherently consistent —
	// no writes can be in flight. Do NOT pass Quiesce (there is no live agent;
	// libvirt's quiesce flag would fail against it and trigger the silent
	// fallback path).
	opts.Quiesce = false
	var err error
	entry, err = CreateSnapshot(opts)
	if err != nil {
		return nil, err
	}
	if domFound && !domRunning && mode == "external" {
		fmt.Fprintf(os.Stderr, "note: vm %q is stopped — the consistent snapshot needs no guest-agent fsfreeze (stopped state is inherently consistent)\n", opts.VmName)
	}
	return entry, nil
}

// --- revert-and-start --------------------------------------------------

// revertAndStartVm implements the body of revert-and-start: stop the VM if
// running -> revert the snapshot -> start the VM. The stop is REQUIRED for the
// revert to be able to run at all: qemu-img (internal mode) refuses to mutate a
// live qcow2, and libvirt's external revert also requires the domain offline
// (revertExternalSnapshot). Binds the real primitives; the ORDER is what the
// seam (revertAndStartSteps) pins under test.
func revertAndStartVm(entity, snapName string) error {
	return (revertAndStartSteps{
		stop:   stopVmDomain,
		revert: RevertSnapshot,
		start:  startVmDomain,
	}).run(entity, snapName)
}

// revertAndStartSteps is the three-primitive orchestration of revert-and-start
// with the primitives INJECTABLE so the order contract is unit-testable without
// a live libvirt/qemu (production binds the package-level impls). The order is
// load-bearing: revert MUST run only after the domain is stopped (qemu-img
// refuses a live qcow2; libvirt external revert needs the domain offline), and
// start MUST run only after the revert succeeded.
type revertAndStartSteps struct {
	stop   func(name string, force bool) (bool, error)
	revert func(vmName, snapName string) error
	start  func(name string) error
}

func (s revertAndStartSteps) run(entity, snapName string) error {
	if entity == "" {
		return fmt.Errorf("revert-and-start: vm name is required")
	}
	if snapName == "" {
		return fmt.Errorf("revert-and-start: snapshot name is required")
	}
	// Stop first (graceful; force=false preserves the "stopped, not depleted"
	// semantic — the disk + definition survive for the revert + restart). An
	// already-stopped domain is a clean no-op (stopped=true); an absent VM must
	// FAIL, never false-succeed (the #77 stop / #69 destroy class).
	name := vmName(entity, "")
	stopped, err := s.stop(name, false)
	if err != nil {
		return err
	}
	if !stopped {
		return fmt.Errorf("no such VM %q: no libvirt domain and no qemu state — nothing to revert-and-start", name)
	}
	// The domain is now offline: internal revert (qemu-img snapshot -a) can
	// mutate the qcow2 and external revert can rebase the inactive domain.
	if err := s.revert(entity, snapName); err != nil {
		return err
	}
	// Bring the VM back up from whichever backend ACTUALLY holds it.
	return s.start(name)
}
