package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/opencharly/sdk/vmshared"
	"github.com/opencharly/spec/spec"
)

// vm_backend_qemu.go — the `vmbackend:qemu` provider: a stored qemu argv in a
// per-domain state dir. Moved behind the class; the behavior is unchanged.
type qemuBackend struct{}

func (qemuBackend) Word() spec.VmBackend { return vmBackendQemu }

func (qemuBackend) AutoDetectable() bool { return true }

// SupportsPCIHostdev is false: a bare qemu process has no domain XML to carry a
// PCI <hostdev>.
func (qemuBackend) SupportsPCIHostdev() bool { return false }

func (qemuBackend) Available() error {
	qemuBin := vmshared.QemuSystemBinary()
	if _, err := exec.LookPath(qemuBin); err != nil {
		return fmt.Errorf("qemu backend requires %s", qemuBin)
	}
	return nil
}

// Holder claims a state dir only when it carries a qemu marker (`command` from
// `vm create`, or `qemu.pid` once started). A krun guest uses the SAME
// per-domain state dir for its ssh material but writes `krun-plan.json`, and an
// unmarked directory is not a VM at all — the previous "any directory is qemu"
// probe would have claimed a krun guest and torn it down through the wrong arm.
func (qemuBackend) Holder(name string) (found, running bool, stateDir string) {
	dir, err := vmDir()
	if err != nil {
		return false, false, ""
	}
	stateDir = filepath.Join(dir, name)
	if !qemuStateDirMarked(stateDir) {
		return false, false, ""
	}
	return true, vmshared.QemuAlive(stateDir), stateDir
}

// qemuStateDirMarked reports whether a per-domain state dir carries a qemu
// artifact (the stored `command` or a `qemu.pid`). Extracted so the probe can
// be tested without a live process.
func qemuStateDirMarked(stateDir string) bool {
	for _, marker := range []string{"command", "qemu.pid"} {
		if _, err := os.Stat(filepath.Join(stateDir, marker)); err == nil {
			return true
		}
	}
	return false
}

func (qemuBackend) Start(name string) (bool, error) {
	dir, err := vmDir()
	if err != nil {
		return false, err
	}
	if vmshared.QemuAlive(filepath.Join(dir, name)) {
		return true, nil
	}
	if err := startQemuFromStateDir(name, filepath.Join(dir, name)); err != nil {
		return false, err
	}
	return false, nil
}

func (qemuBackend) Stop(name string, force bool) error {
	_, _, stateDir := qemuBackend{}.Holder(name)
	if stateDir == "" {
		return nil
	}
	if force {
		// Try QMP quit first, fall back to process kill.
		if err := qemuForceShutdown(stateDir); err != nil {
			killQemuByPID(stateDir)
		}
		return nil
	}
	if err := qemuGracefulShutdown(stateDir); err != nil {
		pidFile := filepath.Join(stateDir, "qemu.pid")
		if data, readErr := os.ReadFile(pidFile); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil {
				if proc, findErr := os.FindProcess(pid); findErr == nil {
					_ = proc.Signal(syscall.SIGTERM)
				}
			}
		}
	}
	return nil
}

func (qemuBackend) Destroy(name, stateDir string, _ bool) (bool, error) {
	if stateDir == "" {
		_, _, stateDir = qemuBackend{}.Holder(name)
	}
	if stateDir == "" {
		return false, nil
	}
	if err := qemuForceShutdown(stateDir); err != nil {
		killQemuByPID(stateDir)
	}
	if err := os.RemoveAll(stateDir); err != nil {
		return false, fmt.Errorf("removing qemu state dir %s: %w", stateDir, err)
	}
	return true, nil
}

func (qemuBackend) Create(req vmCreateReq) error {
	return runVmSpecCreateQemu(req.Spec, req.RT, req.VmDomainName, req.Home, req.VmName, req.Name, req.VmStateDir)
}

func (qemuBackend) RenderForValidation(vmCreateReq) (string, error) { return "", nil }

func (qemuBackend) Screenshot(name, out string) error { return qemuScreendump(name, out) }

func (qemuBackend) Console(name string, _ bool, _ int) error {
	dir, err := vmDir()
	if err != nil {
		return err
	}
	monitorSocket := filepath.Join(dir, name, "monitor.sock")
	if _, err := os.Stat(monitorSocket); err != nil {
		return fmt.Errorf("VM %s monitor socket not found — is the VM running?", name)
	}
	return connectUnixConsole(monitorSocket)
}
