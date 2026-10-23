package vm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/opencharly/sdk/vmshared"
	"github.com/opencharly/spec/spec"
)

// vm_backend_libvirt.go — the `vmbackend:libvirt` provider. The behavior is the
// existing libvirt arms, moved behind the class so no caller switches on the
// word. Every probe/op still goes through the same in-package libvirt engine
// (`invokeVmPluginEnv`).
type libvirtBackend struct{}

func (libvirtBackend) Word() spec.VmBackend { return vmBackendLibvirt }

func (libvirtBackend) AutoDetectable() bool { return true }

// SupportsPCIHostdev is true: libvirt renders a PCI <hostdev>.
func (libvirtBackend) SupportsPCIHostdev() bool { return true }

// Available starts the session daemon BEFORE probing its socket (a cold stat
// alone false-negatives a fully working libvirt — see
// vmshared.StartLibvirtUserSession) and returns an actionable error when the
// operator explicitly asked for libvirt and it is absent. The remediation text
// is load-bearing: operators often see only the last line.
func (libvirtBackend) Available() error {
	vmshared.StartLibvirtUserSession()
	picked, probed := vmshared.LibvirtSessionSocketWithProbes()
	if _, err := os.Stat(picked); err == nil {
		return nil
	}
	var trail strings.Builder
	for _, p := range probed {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(&trail, "\n  %s — found", p)
		} else {
			fmt.Fprintf(&trail, "\n  %s — not found", p)
		}
	}
	return fmt.Errorf(
		"libvirt backend requires libvirt session daemon (probed:%s\n"+
			"configure libvirt session daemon or run: charly settings set vm.backend qemu)",
		trail.String(),
	)
}

func (libvirtBackend) Holder(name string) (found, running bool, stateDir string) {
	if raw, ok := invokeVmPluginEnv(vmPluginEnv{VmOp: "domain-state", VmName: name}); ok && vmPluginOpFlag(raw, "exists") {
		return true, vmPluginOpFlag(raw, "running"), ""
	}
	return false, false, ""
}

func (libvirtBackend) Start(name string) (bool, error) {
	raw, ok := invokeVmPlugin("start", name, "")
	if !ok {
		return false, fmt.Errorf("VM %s: vm plugin unavailable (go-libvirt is out-of-process)", name)
	}
	if e := vmPluginOpError(raw); e != "" {
		return false, fmt.Errorf("starting VM %s: %s", name, e)
	}
	return vmPluginOpFlag(raw, "already_running"), nil
}

func (libvirtBackend) Stop(name string, force bool) error {
	_, running, _ := libvirtBackend{}.Holder(name)
	// shutdownDomain on an already-SHUTOFF domain errors ("domain not running"),
	// and a shutoff domain is already stopped by construction.
	if !running {
		return nil
	}
	raw, ok := invokeVmPluginEnv(vmPluginEnv{VmOp: "stop", VmName: name, Force: force})
	if !ok {
		return fmt.Errorf("VM %s: vm plugin unavailable (go-libvirt is out-of-process)", name)
	}
	if e := vmPluginOpError(raw); e != "" {
		return fmt.Errorf("stopping VM %s: %s", name, e)
	}
	// The libvirt stop op is ASYNC for a graceful stop: shutdownDomain sends ACPI
	// poweroff and returns immediately. Poll for the domain to leave DomainRunning
	// (the same poll gracefulStopDomain uses) and fail only if it is still running
	// after the grace. force=true (destroyDomain) is synchronous.
	cfg := loadedReadiness().StopGate("stop domain " + name)
	if pollErr := pollUntil(context.Background(), cfg, func(context.Context) (bool, float64, error) {
		vr, ok := invokeVmPluginEnv(vmPluginEnv{VmOp: "domain-state", VmName: name})
		return ok && !vmPluginOpFlag(vr, "running"), 0, nil
	}); pollErr != nil {
		return fmt.Errorf("VM %s: did not reach a stopped state within the stop grace (still running) — use 'charly vm stop --force' if the guest ignores ACPI: %w", name, pollErr)
	}
	return nil
}

func (libvirtBackend) Destroy(name, _ string, deleteDisk bool) (bool, error) {
	dr, ok := invokeVmPluginEnv(vmPluginEnv{VmOp: "destroy", VmName: name, DeleteDisk: deleteDisk})
	if !ok {
		return false, fmt.Errorf("VM %s: vm plugin unavailable (go-libvirt is out-of-process)", name)
	}
	if e := vmPluginOpError(dr); e != "" {
		return false, fmt.Errorf("destroying VM %s: %s", name, e)
	}
	// Confirm the domain is really gone: undefining a still-running domain that
	// was NOT force-stopped first leaves a lingering TRANSIENT domain.
	if vr, ok := invokeVmPluginEnv(vmPluginEnv{VmOp: "domain-state", VmName: name}); ok && vmPluginOpFlag(vr, "exists") {
		return false, fmt.Errorf("VM %s: libvirt reported the destroy succeeded but the domain is still defined", name)
	}
	// --disk must also reclaim the per-domain HOST state dir (the live overlay,
	// snapshots/, ssh keys, seed ISO) — not only the repo image/<entity>/ dir.
	if deleteDisk {
		if err := removeVmStateDir(name); err != nil {
			return true, fmt.Errorf("removing VM state dir for %s: %w", name, err)
		}
	}
	return true, nil
}

func (libvirtBackend) Create(req vmCreateReq) error {
	return runVmSpecCreateLibvirt(req.Spec, req.RT, req.VmDomainName, req.Home, req.VmName, req.Name)
}

func (libvirtBackend) RenderForValidation(req vmCreateReq) (string, error) {
	return RenderDomainXML(req.Spec, req.RT)
}

func (libvirtBackend) Screenshot(name, out string) error { return virshScreenshot(name, out) }

func (libvirtBackend) Console(name string, _ bool, _ int) error {
	// Keep virsh console for interactive serial — libvirt console streams are
	// complex; syscall.Exec replaces the process.
	bin, err := exec.LookPath("virsh")
	if err != nil {
		return fmt.Errorf("virsh is required for libvirt console access: %w", err)
	}
	return syscall.Exec(bin, []string{"virsh", "-c", libvirtSessionURI, "console", name}, os.Environ())
}
