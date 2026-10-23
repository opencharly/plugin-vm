package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// vm_backend_krun.go — the `vmbackend:krun` provider: a VM whose root is a
// containerd snapshot, launched through the containerd engine's START PLAN with
// `runtime: krun`. It owns NO libvirt domain and NO qemu process; its state is
// (a) the container and (b) a small JSON record in the per-domain state dir.
//
// STOP IS NEVER A SIGNAL. A systemd guest is powered off with `systemctl
// poweroff` over the managed SSH alias (the image's `krun-poweroff` hook is what
// makes that callable by the login user), and the provider then VERIFIES the
// container left `running`. `--force` does not change that: signalling a
// systemd guest is the defect this rule exists to prevent.
//
// SNAPSHOT is refused pending the W2 seam-2 extraction. Seam 2's commit +
// state-volume-clone lives in `opencharly/crabbox`'s
// `internal/providers/containerd` package, which Go's internal rule forbids
// this module from importing; writing a second implementation would be the R2
// duplication this provider exists to avoid. The refusal names the extraction.
type krunBackend struct{}

// krunStateFile is the per-domain record. It is the marker that distinguishes a
// krun state dir from a qemu one (see qemuStateDirMarked).
const krunStateFile = "krun-plan.json"

// krunState is the persisted launch record. `Argv` is the complete engine argv
// so `start` can re-run it without re-resolving the project config (matching the
// qemu backend's stored `command`).
type krunState struct {
	Name     string   `json:"name"`
	Engine   string   `json:"engine"`
	Image    string   `json:"image"`
	SSHPort  int      `json:"ssh_port"`
	SSHAlias string   `json:"ssh_alias"`
	Systemd  bool     `json:"systemd"`
	Argv     []string `json:"argv"`
}

func (krunBackend) Word() spec.VmBackend { return vmBackendKrun }

// AutoDetectable is false: krun is explicit-only, exactly as
// deploykit.ResolveVmBackend documents. `backend: auto` never selects it.
func (krunBackend) AutoDetectable() bool { return false }

// SupportsPCIHostdev is false: a krun guest has no PCI hostdev; its GPU path is
// virtio-gpu (and NVIDIA is refused upstream).
func (krunBackend) SupportsPCIHostdev() bool { return false }

func (krunBackend) Available() error {
	_, _, _, err := krunPreflight()
	return err
}

func (krunBackend) Holder(name string) (found, running bool, stateDir string) {
	dir, err := vmDir()
	if err != nil {
		return false, false, ""
	}
	stateDir = filepath.Join(dir, name)
	if _, err := os.Stat(krunStatePath(stateDir)); err != nil {
		return false, false, ""
	}
	return true, krunContainerRunning(name), stateDir
}

func (krunBackend) Start(name string) (bool, error) {
	stateDir, err := krunStateDirFor(name)
	if err != nil {
		return false, err
	}
	state, err := readKrunState(stateDir)
	if err != nil {
		return false, err
	}
	if krunContainerRunning(name) {
		return true, nil
	}
	// A stopped container must be `start`ed: `run` refuses a name already in use,
	// and the guest's writable snapshot is the container's, not the image's. Only
	// an ABSENT container re-runs the recorded launch argv.
	if krunContainerExists(name) {
		if err := krunContainerStart(name); err != nil {
			return false, fmt.Errorf("starting krun guest %s: %w", name, err)
		}
		return false, nil
	}
	if err := runKrunArgv(state.Argv); err != nil {
		return false, fmt.Errorf("starting krun guest %s: %w", name, err)
	}
	return false, nil
}

func (krunBackend) Stop(name string, _ bool) error {
	stateDir, err := krunStateDirFor(name)
	if err != nil {
		return err
	}
	state, err := readKrunState(stateDir)
	if err != nil {
		return err
	}
	if !krunContainerRunning(name) {
		return nil
	}
	// Power the guest off through its own init. The image's `krun-poweroff` hook
	// is the image-side half; this is the host-side call.
	if err := krunPoweroffOverSSH(state.SSHAlias); err != nil {
		return fmt.Errorf(
			"stopping krun guest %s: %w (never signalling a systemd guest; fix SSH/the image's krun-poweroff hook, or destroy the deploy)",
			name, err)
	}
	cfg := loadedReadiness().StopGate("stop krun guest " + name)
	if pollErr := pollUntil(context.Background(), cfg, func(context.Context) (bool, float64, error) {
		return !krunContainerRunning(name), 0, nil
	}); pollErr != nil {
		return fmt.Errorf(
			"krun guest %s accepted `systemctl poweroff` but its container is still running within the stop grace; "+
				"the guest did not power off (no signal is sent by design): %w", name, pollErr)
	}
	return nil
}

func (krunBackend) Destroy(name, stateDir string, deleteDisk bool) (bool, error) {
	if stateDir == "" {
		var err error
		stateDir, err = krunStateDirFor(name)
		if err != nil {
			return false, err
		}
	}
	if _, err := os.Stat(krunStatePath(stateDir)); err != nil {
		return false, nil
	}
	// `rm -f` is the explicit teardown (a STOP is the SSH poweroff path). Skip it
	// when the container is already absent (a failed create leaves only the
	// record): `nerdctl rm` on a missing container errors, which would otherwise
	// make the leftover record unclearable.
	if krunContainerExists(name) {
		if err := krunContainerRemove(name); err != nil {
			return false, fmt.Errorf("removing krun container %s: %w", name, err)
		}
	}
	if deleteDisk {
		if err := os.RemoveAll(stateDir); err != nil {
			return true, fmt.Errorf("removing krun state dir %s: %w", stateDir, err)
		}
	} else {
		// Keep the state dir (ssh key, known_hosts) but drop the launch record so
		// the VM is no longer reported as existing.
		_ = os.Remove(krunStatePath(stateDir))
	}
	return true, nil
}

func (krunBackend) Create(req vmCreateReq) error {
	engine, _, target, err := krunPreflight()
	if err != nil {
		return err
	}
	image, err := krunImageRef(req.Spec, engine)
	if err != nil {
		return err
	}
	stateDir := req.VmStateDir
	if stateDir == "" {
		if stateDir, err = krunStateDirFor(req.VmDomainName); err != nil {
			return err
		}
	}
	plan, err := buildKrunPlan(krunPlanInput{
		Name:         req.VmDomainName,
		Image:        image,
		Engine:       engine,
		Target:       target,
		Spec:         req.Spec,
		SSHPort:      req.RT.SSHPort,
		PortForwards: req.RT.ExtraPortForwards,
		SSHPubKey:    req.KrunSSHPubKey,
		Systemd:      krunGuestSystemd(req.Spec),
		NVIDIA:       req.KrunNVIDIA,
	})
	if err != nil {
		return err
	}
	argv, err := krunStartArgv(plan, target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("creating krun state dir %s: %w", stateDir, err)
	}
	state := krunState{
		Name:     plan.Name,
		Engine:   plan.Engine,
		Image:    plan.Image,
		SSHPort:  plan.SSHPort,
		SSHAlias: plan.Name,
		Systemd:  req.Spec != nil && krunGuestSystemd(req.Spec),
		Argv:     argv,
	}
	if err := writeKrunState(stateDir, state); err != nil {
		return err
	}
	if err := runKrunArgv(argv); err != nil {
		return fmt.Errorf("creating krun guest %s: %w", req.VmDomainName, err)
	}
	return nil
}

func (krunBackend) RenderForValidation(vmCreateReq) (string, error) {
	// A krun guest has no libvirt domain XML. Its OCI image is the build's
	// canonical artefact and is egress-validated at build time; there is no
	// second render to validate here.
	return "", nil
}

func (krunBackend) Screenshot(name, _ string) error {
	return fmt.Errorf("a krun guest exposes no host-capturable framebuffer: crun/krun does not expose the virtio-gpu display, so %s cannot be screenshotted; use `charly vm console %s --dump` for the guest log", name, name)
}

func (krunBackend) Console(name string, dump bool, lines int) error {
	if !dump {
		return fmt.Errorf(
			"a krun guest has no serial console device to attach to: its console is the container's stdio. "+
				"Use `charly vm console %s --dump` for the log, or `charly vm ssh %s`", name, name)
	}
	argv, err := krunEngineArgv("logs")
	if err != nil {
		return err
	}
	argv = append(argv, "--tail", fmt.Sprintf("%d", lines), name)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runKrunCreate is the krun create orchestration: resolve the SSH host port
// (persisting the auto allocation so the create → deploy-add sequence reuses
// it), hand the fully host-resolved request to the provider, then publish the
// managed ssh alias. It deliberately does NOT touch any disk/seed/OVMF path —
// a krun guest's root is a containerd snapshot.
func (c *VmCreateCmd) runKrunCreate(entity, domainID, name, vmDomainName, vmStateDir string, spec *VmSpec, vmState *VmDeployState, perDomain bool, cnode *DeployNode, resources map[string]*ResolvedResource) error {
	nvidia := krunNVIDIARequired(cnode, resources)
	if err := krunRefuseNVIDIA(nvidia); err != nil {
		return err
	}
	if err := os.MkdirAll(vmStateDir, 0o755); err != nil {
		return fmt.Errorf("creating krun state dir %s: %w", vmStateDir, err)
	}
	// Generate/select the per-domain key pair so the managed ssh alias has an
	// identity, and hand the PUBLIC half to the guest through the plan env.
	pubKey, err := resolveSSHPubKeyForSpec(spec, vmStateDir)
	if err != nil {
		return fmt.Errorf("resolving the SSH key: %w", err)
	}
	sshPort, err := resolveVmSshPort(spec, domainID)
	if err != nil {
		return fmt.Errorf("resolving SSH port: %w", err)
	}
	// Resolve network.port_forwards through the ONE shared resolver (seeding the
	// occupied set with the SSH port), and persist the auto allocations so the
	// create → deploy-add sequence reuses them — exactly as the libvirt/qemu path
	// does. A krun guest must not silently drop authored forwards.
	occupied := map[int]bool{}
	if sshPort > 0 {
		occupied[sshPort] = true
	}
	resolvedForwards, allocatedForwards, err := resolveVmPortForwards(spec, domainID, occupied)
	if err != nil {
		return fmt.Errorf("resolving port forwards: %w", err)
	}
	if (spec.SSH != nil && spec.SSH.PortAuto) || len(allocatedForwards) > 0 {
		st := &VmDeployState{}
		if vmState != nil {
			st = vmState
		}
		if spec.SSH != nil && spec.SSH.PortAuto {
			st.SSHPort = sshPort
		}
		if len(allocatedForwards) > 0 {
			st.PortForwards = allocatedForwards
		}
		if err := hostConfigPersist(domainID, entity, st, false); err != nil {
			return fmt.Errorf("persisting auto-allocated ports: %w", err)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rt := VmRuntimeParams{SSHPort: sshPort, ExtraPortForwards: resolvedForwards, VmStateDir: vmStateDir}
	req := vmCreateReq{
		Spec:          spec,
		RT:            rt,
		VmDomainName:  vmDomainName,
		Home:          home,
		VmName:        entity,
		Name:          name,
		Backend:       string(vmBackendKrun),
		VmStateDir:    vmStateDir,
		KrunNVIDIA:    nvidia,
		KrunSSHPubKey: pubKey,
	}
	if err := (krunBackend{}).Create(req); err != nil {
		return err
	}
	if perDomain {
		migrateStaleEntityAlias(home, entity)
	}
	if err := publishVmSshAlias(home, name, spec, rt); err != nil {
		return fmt.Errorf("publishing ssh-config alias: %w", err)
	}
	return nil
}

// nvidiaPCIVendor is the canonical PCI vendor id NormalizePCIVendor emits.
const nvidiaPCIVendor = "0x10de"

// krunNVIDIARequired reports whether the claimant requires an NVIDIA GPU. The
// krun provider refuses it with the upstream reason (the render-server sandbox
// dev-binds only /dev/dri).
func krunNVIDIARequired(cnode *DeployNode, resources map[string]*ResolvedResource) bool {
	_, selector, ok := requiredGPUResource(cnode, resources)
	if !ok || selector == nil {
		return false
	}
	return normalizePCIVendor(selector.Vendor) == nvidiaPCIVendor
}

// krunGuestSystemd reports whether the guest init is systemd. Both krun-capable
// source arms are bootable OS images: `bootc` is always a systemd OS, and a
// `container_disk` used as a krun root is a guest rootfs. The image's
// `ai.opencharly.init_def` label is the general contract, but reading it needs
// the W3.7 image resolver, which has not landed; this default is the documented
// krun guest contract, not an inference.
func krunGuestSystemd(s *VmSpec) bool {
	if s == nil {
		return true
	}
	switch s.Source.Kind {
	case "bootc", "container_disk":
		return true
	default:
		return false
	}
}

// krunImageRef resolves the OCI image a krun guest boots. The schema's krun
// cross-rule already restricts the source to these two arms; the error here is
// the runtime backstop.
func krunImageRef(s *VmSpec, engine string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("vmbackend:krun requires a kind:vm spec")
	}
	switch s.Source.Kind {
	case "container_disk":
		if s.Source.Image == "" {
			return "", fmt.Errorf("vmbackend:krun: container_disk source has no image")
		}
		return s.Source.Image, nil
	case "bootc":
		return resolveBootcImageRef(engine, s.Source.Box)
	default:
		return "", fmt.Errorf(
			"vmbackend:krun requires an OCI-image source (`container_disk` or `bootc`), got %q", s.Source.Kind)
	}
}

// krunStateDirFor resolves the per-domain state dir for a VM name.
func krunStateDirFor(name string) (string, error) {
	root, err := vmDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

func krunStatePath(stateDir string) string { return filepath.Join(stateDir, krunStateFile) }

func writeKrunState(stateDir string, state krunState) error {
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding krun state: %w", err)
	}
	path := krunStatePath(stateDir)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("writing krun state %s: %w", path, err)
	}
	return nil
}

func readKrunState(stateDir string) (krunState, error) {
	path := krunStatePath(stateDir)
	body, err := os.ReadFile(path)
	if err != nil {
		return krunState{}, fmt.Errorf("reading krun state %s: %w", path, err)
	}
	var state krunState
	if err := json.Unmarshal(body, &state); err != nil {
		return krunState{}, fmt.Errorf("decoding krun state %s: %w", path, err)
	}
	return state, nil
}

// krunEngineArgv returns the engine CLI argv prefix: the binary plus the
// target's global flags (address, namespace) and the rootful privilege prefix.
// It is the ONE place the krun provider assembles engine addressing.
func krunEngineArgv(subcommand string) ([]string, error) {
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	target, err := deploykit.ResolveContainerdTarget(spec.EngineName(rt.RunEngine), deploykit.Selection{}, rt)
	if err != nil {
		return nil, err
	}
	capability, ok := container.EngineCapabilityFor(rt.RunEngine)
	if !ok || capability.Binary == "" {
		return nil, fmt.Errorf("containerd engine %q has no binary", rt.RunEngine)
	}
	return krunApplyTarget([]string{capability.Binary, subcommand}, target), nil
}

// The engine/SSH seams are variables so the lifecycle tests drive the sequence
// with recording fakes (the same pattern as vmshared.StartLibvirtUserSession in
// the libvirt tests).
var (
	runKrunArgv          = runKrunArgvReal
	krunContainerRunning = krunContainerRunningReal
	krunContainerExists  = krunContainerExistsReal
	krunContainerStart   = krunContainerStartReal
	krunContainerRemove  = krunContainerRemoveReal
	krunPoweroffOverSSH  = krunPoweroffOverSSHReal
)

func runKrunArgvReal(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty krun launch argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// krunContainerRunning reports whether the container exists and is running. A
// probe failure (no engine, no handler) is NOT "running": an unavailable engine
// cannot have a live guest.
func krunContainerRunningReal(name string) bool {
	argv, err := krunEngineArgv("inspect")
	if err != nil {
		return false
	}
	argv = append(argv, "--format", "{{.State.Status}}", name)
	out, err := exec.Command(argv[0], argv[1:]...).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "running"
}

// krunContainerExists reports whether the container is present in ANY state
// (a stopped container still occupies its name and its writable snapshot).
func krunContainerExistsReal(name string) bool {
	argv, err := krunEngineArgv("inspect")
	if err != nil {
		return false
	}
	argv = append(argv, name)
	return exec.Command(argv[0], argv[1:]...).Run() == nil
}

func krunContainerStartReal(name string) error {
	argv, err := krunEngineArgv("start")
	if err != nil {
		return err
	}
	argv = append(argv, name)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func krunContainerRemoveReal(name string) error {
	argv, err := krunEngineArgv("rm")
	if err != nil {
		return err
	}
	argv = append(argv, "-f", name)
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// krunPoweroffOverSSH is the ONLY stop mechanism. BatchMode makes a missing
// key/agent a fast, explicit failure rather than a prompt; ConnectTimeout bounds
// the connection. There is no fallback to a signal.
func krunPoweroffOverSSHReal(alias string) error {
	if alias == "" {
		return fmt.Errorf("no SSH alias recorded for this krun guest")
	}
	cmd := exec.Command("ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		alias,
		"systemctl", "poweroff")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
