package vm

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/ops"
	"github.com/opencharly/spec/spec"
)

// vm_krun_plan.go — the PURE `vmbackend:krun` mapping: VmSpec + the resolved
// containerd target -> an engine start-plan request -> argv. It is pure so the
// mapping (the whole risk of this backend) is tested without a krun handler.
//
// The mapping, verbatim from the plan's W3.4:
//
//	ram             -> --memory <ram>, --memory-swap <ram>
//	                   AND krun.ram_mib = ram - ram_headroom_mib
//	                   (the guest RAM is NOT the container limit: the container
//	                   limit also covers libkrun's host overhead)
//	cpu             -> krun.cpus (--cpus is NOT passed: it sets no vCPUs)
//	krun.network: passt -> krun.use_passt=1
//	krun.kernel: image  -> krun.custom_kernel=1
//	systemd guest   -> KRUN_INIT_PID1=1 + --ulimit nofile=524288:524288
//	ssh             -> a published loopback port 127.0.0.1:<host>:22
//
// `--memory`, `--memory-swap`, `--ulimit` and `--annotation` ride the start
// plan's `extra_args`, which the engine appends verbatim: the start-plan
// envelope carries no memory/ulimit/annotation fields yet (W3.3's extension),
// so this is the contract's existing carrier, not a second argv builder.

const (
	// krunRuntimeHandler is the OCI runtime handler word the containerd
	// capability must declare. It is the provider's own class word (its identity),
	// not a dispatch branch.
	krunRuntimeHandler = string(vmBackendKrun)

	// krunDefaultGPUFlags is libkrun's measured working virtio-gpu feature bitmask
	// (venus + the working set). The schema declares the same default; this is the
	// consumer-side fallback when the `krun:` block is absent entirely.
	krunDefaultGPUFlags = 962

	// krunDefaultRAMHeadroomMiB is the measured libkrun host overhead. The schema
	// declares the same default on #VmKrun; this is the consumer-side fallback when
	// the block is absent.
	krunDefaultRAMHeadroomMiB = 256

	// krunDefaultRAMMiB matches the VM path's long-standing 4G default.
	krunDefaultRAMMiB = 4096

	// krunDefaultCPUs is the VM path's long-standing single-vCPU default.
	krunDefaultCPUs = 1

	// krunSystemdNoFileUlimit raises PID 1's file-descriptor limit. A systemd
	// guest under krun needs it (KRUN_INIT_PID1 re-execs the init as PID 1, and
	// the container's default nofile limit is too low for udev/systemd).
	krunSystemdNoFileUlimit = "nofile=524288:524288"

	// krunGPUComputeFloorMiB is the guest-RAM floor below which Vulkan compute in
	// a krun GPU guest is OOM-killed. Measured: at 1 GiB the guest OOM-kills
	// Vulkan compute; a GPU guest needs the caller to raise ram:, never to lower
	// the headroom.
	krunGPUComputeFloorMiB = 1024

	// krunSSHPort is the guest sshd port the loopback host port is published to.
	krunSSHPort = 22
)

// krunHandlerRuntimes are the runtime binaries a containerd `krun` handler is
// configured with: crun built with `--with-libkrun` (layer-krun installs
// `crun`). Probed so the skip is honest on a host where containerd is selected
// but the handler's runtime is not installed — the capability table declares
// krun for every containerd, which is a claim, not a host fact.
var krunHandlerRuntimes = []string{"crun", "crun-krun"}

func krunHandlerRuntimeInstalled() bool {
	for _, bin := range krunHandlerRuntimes {
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
	}
	return false
}

// krunAnnotation is one `--annotation k=v` pair. A slice, not a map, so argv
// order is deterministic and testable.
type krunAnnotation struct {
	Key   string
	Value string
}

// krunPlan is the fully mapped launch. It carries both the container limit and
// the guest RAM so the two can never be conflated.
type krunPlan struct {
	Name         string
	Image        string
	Engine       string
	Runtime      string
	ContainerMiB int
	GuestMiB     int
	Cpus         int
	SSHPort      int
	Ports        []string
	Env          map[string]string
	Annotations  []krunAnnotation
	ExtraArgs    []string
}

// krunPlanInput is everything the mapping needs. NVIDIA is resolved by the
// caller (the create path has the claimant's resources) because the refusal is
// about the HOST's GPU intent, not only the krun block.
type krunPlanInput struct {
	Name         string
	Image        string
	Engine       string
	Target       deploykit.ContainerdTarget
	Spec         *VmSpec
	SSHPort      int
	PortForwards []string // resolved "<host>:<guest>" entries from network.port_forwards
	SSHPubKey    string   // the per-domain public key the guest must authorize
	Systemd      bool
	NVIDIA       bool
}

// krunSettings applies the schema's defaults when the `krun:` block is absent.
// When the block is present, ApplyCueDefaults has already materialized them; the
// zero-value fills below keep a hand-decoded spec correct too.
func krunSettings(s *VmSpec) spec.VmKrun {
	k := spec.VmKrun{
		Network:        "tsi",
		Kernel:         "stock",
		GPU:            "off",
		GPUFlags:       krunDefaultGPUFlags,
		RAMHeadroomMiB: krunDefaultRAMHeadroomMiB,
	}
	if s == nil || s.Krun == nil {
		return k
	}
	got := *s.Krun
	if got.Network != "" {
		k.Network = got.Network
	}
	if got.Kernel != "" {
		k.Kernel = got.Kernel
	}
	if got.GPU != "" {
		k.GPU = got.GPU
	}
	if got.GPUFlags != 0 {
		k.GPUFlags = got.GPUFlags
	}
	if got.RAMHeadroomMiB != 0 {
		k.RAMHeadroomMiB = got.RAMHeadroomMiB
	}
	k.NestedVirt = got.NestedVirt
	return k
}

// krunGuestRam splits the authored RAM into the container limit and the guest
// RAM. The container limit is the authored `ram`; the guest sees it MINUS the
// host headroom. This split is the mapping most likely to be "simplified" away,
// so it is a named function with a failing-without test.
func krunGuestRam(s *VmSpec, headroomMiB int) (containerMiB, guestMiB int, err error) {
	containerMiB = krunDefaultRAMMiB
	if s != nil {
		if v := parseRAMtoMB(resolveVmRam(s)); v > 0 {
			containerMiB = v
		}
	}
	if headroomMiB < 0 {
		return 0, 0, fmt.Errorf("krun.ram_headroom_mib must be >= 0, got %d", headroomMiB)
	}
	guestMiB = containerMiB - headroomMiB
	if guestMiB <= 0 {
		return 0, 0, fmt.Errorf(
			"krun guest RAM must exceed the %d MiB host headroom (container --memory is %d MiB); raise `ram:` or lower `krun.ram_headroom_mib`",
			headroomMiB, containerMiB)
	}
	return containerMiB, guestMiB, nil
}

// krunRefuseNVIDIA is the krun + NVIDIA refusal (the same one the crabbox
// provider carries): the render-server sandbox dev-binds only /dev/dri, so a
// krun guest cannot see the NVIDIA device nodes. This is a real upstream limit,
// not a charly bug — NEEDS UPSTREAM.
func krunRefuseNVIDIA(nvidia bool) error {
	if !nvidia {
		return nil
	}
	return fmt.Errorf(
		"krun + NVIDIA is refused: the render-server sandbox dev-binds only /dev/dri, so a krun guest cannot see the NVIDIA character devices (NEEDS UPSTREAM). " +
			"Use backend libvirt for NVIDIA passthrough, or drop the GPU resource for this deploy")
}

// krunRefuseGPUFloor is the measured guest-OOM floor: a virtio-GPU krun guest
// below 1024 MiB of GUEST RAM OOM-kills Vulkan compute.
func krunRefuseGPUFloor(s *VmSpec, guestMiB int) error {
	if krunSettings(s).GPU != "virtio" {
		return nil
	}
	if guestMiB >= krunGPUComputeFloorMiB {
		return nil
	}
	return fmt.Errorf(
		"krun virtio GPU needs at least %d MiB of guest RAM (after the host headroom), got %d MiB: below the floor the guest OOM-kills Vulkan compute. Raise `ram:` (not the headroom)",
		krunGPUComputeFloorMiB, guestMiB)
}

// buildKrunPlan is the ONE mapping. It refuses before it builds anything, so a
// refused guest never reaches the engine.
func buildKrunPlan(in krunPlanInput) (krunPlan, error) {
	k := krunSettings(in.Spec)
	containerMiB, guestMiB, err := krunGuestRam(in.Spec, k.RAMHeadroomMiB)
	if err != nil {
		return krunPlan{}, err
	}
	if err := krunRefuseNVIDIA(in.NVIDIA); err != nil {
		return krunPlan{}, err
	}
	if err := krunRefuseGPUFloor(in.Spec, guestMiB); err != nil {
		return krunPlan{}, err
	}
	cpus := 0
	if in.Spec != nil {
		cpus = resolveVmCpus(in.Spec)
	}
	if cpus <= 0 {
		cpus = krunDefaultCPUs
	}

	plan := krunPlan{
		Name:         in.Name,
		Image:        in.Image,
		Engine:       in.Engine,
		Runtime:      krunRuntimeHandler,
		ContainerMiB: containerMiB,
		GuestMiB:     guestMiB,
		Cpus:         cpus,
		SSHPort:      in.SSHPort,
		Env:          map[string]string{},
	}

	if in.SSHPort > 0 {
		// Published to LOOPBACK only: the guest's sshd is reached through the
		// host's loopback, like every other charly VM.
		plan.Ports = []string{fmt.Sprintf("127.0.0.1:%d:%d", in.SSHPort, krunSSHPort)}
	}
	// Authored network.port_forwards are published the same way (the libvirt/qemu
	// path maps them as domain forwards; a krun guest must not silently drop
	// them). The resolver has already allocated any `auto:` host side.
	for _, forward := range in.PortForwards {
		plan.Ports = append(plan.Ports, "127.0.0.1:"+forward)
	}
	if in.Systemd {
		plan.Env["KRUN_INIT_PID1"] = "1"
	}
	if in.SSHPubKey != "" {
		// A krun guest has no cloud-init seed to carry the key. The image contract
		// installs this env value into the SSH user's authorized_keys; the host-side
		// half is this env, and the image-side half is the image's responsibility.
		plan.Env["KRUN_SSH_PUBKEY"] = in.SSHPubKey
	}

	plan.Annotations = []krunAnnotation{
		{Key: "krun.ram_mib", Value: strconv.Itoa(guestMiB)},
		{Key: "krun.cpus", Value: strconv.Itoa(cpus)},
	}
	if k.Network == "passt" {
		plan.Annotations = append(plan.Annotations, krunAnnotation{Key: "krun.use_passt", Value: "1"})
	}
	if k.Kernel == "image" {
		plan.Annotations = append(plan.Annotations, krunAnnotation{Key: "krun.custom_kernel", Value: "1"})
	}
	if k.NestedVirt {
		plan.Annotations = append(plan.Annotations, krunAnnotation{Key: "krun.nested_virt", Value: "1"})
	}
	if k.GPU == "virtio" {
		plan.Annotations = append(plan.Annotations, krunAnnotation{Key: "krun.gpu_flags", Value: strconv.Itoa(k.GPUFlags)})
	}

	// The container limit is the authored RAM; swap is DISABLED by setting it to
	// the same value. The guest RAM rides krun.ram_mib (above), never --memory.
	plan.ExtraArgs = []string{
		"--memory", fmt.Sprintf("%dm", containerMiB),
		"--memory-swap", fmt.Sprintf("%dm", containerMiB),
	}
	if in.Systemd {
		plan.ExtraArgs = append(plan.ExtraArgs, "--ulimit", krunSystemdNoFileUlimit)
	}
	for _, a := range plan.Annotations {
		plan.ExtraArgs = append(plan.ExtraArgs, "--annotation", a.Key+"="+a.Value)
	}
	return plan, nil
}

// krunStartPlanRequest is the engine-class request for the plan. `Runtime` is
// the krun handler; the engine servant validates it against the capability's
// declared handlers and refuses an undeclared one.
func krunStartPlanRequest(plan krunPlan) spec.EngineStartPlanRequest {
	return spec.EngineStartPlanRequest{
		Name:      plan.Name,
		Image:     plan.Image,
		Engine:    plan.Engine,
		Runtime:   plan.Runtime,
		Detach:    true,
		Ports:     plan.Ports,
		Env:       spec.StrMap(plan.Env),
		ExtraArgs: plan.ExtraArgs,
	}
}

// krunStartArgv asks the engine class for the launch argv and applies the
// containerd target's addressing. The target's `--address`/`--namespace` are
// GLOBAL flags, so they go after the binary and before `run`; the rootful
// privilege prefix goes before the binary.
func krunStartArgv(plan krunPlan, target deploykit.ContainerdTarget) ([]string, error) {
	params, err := json.Marshal(krunStartPlanRequest(plan))
	if err != nil {
		return nil, fmt.Errorf("marshalling krun start plan: %w", err)
	}
	out, err := container.InvokeEngineOp(plan.Engine, ops.OpEngineStartPlan, params)
	if err != nil {
		return nil, fmt.Errorf("krun start plan: %w", err)
	}
	var rep spec.EngineStartPlanReply
	if err := json.Unmarshal(out, &rep); err != nil {
		return nil, fmt.Errorf("decoding krun start plan reply: %w", err)
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("krun start plan: %s", rep.Error)
	}
	if len(rep.Argv) == 0 {
		return nil, fmt.Errorf("krun start plan returned an empty argv")
	}
	argv := krunApplyTarget(rep.Argv, target)
	return argv, nil
}

// krunApplyTarget inserts the target's global engine flags. Extracted so the
// prefix/address/namespace placement is testable without an engine.
func krunApplyTarget(argv []string, target deploykit.ContainerdTarget) []string {
	if len(argv) == 0 {
		return argv
	}
	out := make([]string, 0, len(argv)+4)
	if target.PrivilegePrefix != "" {
		out = append(out, strings.Fields(target.PrivilegePrefix)...)
	}
	out = append(out, argv[0])
	if target.Address != "" {
		out = append(out, "--address", target.Address)
	}
	if target.Namespace != "" {
		out = append(out, "--namespace", target.Namespace)
	}
	out = append(out, argv[1:]...)
	return out
}

// krunPreflight resolves the engine and the containerd target the krun backend
// needs. It is THE visible skip: when no engine declares the krun handler
// (which is the state of every host without libkrun installed), the error names
// the missing handler and the setting that selects containerd.
func krunPreflight() (string, spec.EngineCapability, deploykit.ContainerdTarget, error) {
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return "", spec.EngineCapability{}, deploykit.ContainerdTarget{}, err
	}
	engine := rt.RunEngine
	cap, ok := container.EngineCapabilityFor(engine)
	if !ok {
		return "", spec.EngineCapability{}, deploykit.ContainerdTarget{}, fmt.Errorf(
			"vmbackend:krun: the resolved engine %q has no capability row", engine)
	}
	if !slices.Contains(cap.RuntimeHandlers, krunRuntimeHandler) {
		return "", cap, deploykit.ContainerdTarget{}, fmt.Errorf(
			"vmbackend:krun: the resolved engine %q does not declare the %q runtime handler (declares %v); "+
				"the krun handler (libkrun/libkrunfw + the containerd krun runtime config) is NOT available on this host — "+
				"install the krun host layer and run: charly settings set engine.run containerd",
			engine, krunRuntimeHandler, cap.RuntimeHandlers)
	}
	// The capability table is a CLAIM about the engine; the host must also have
	// the engine CLI and the handler's runtime. On a host where containerd is
	// selected but crun/libkrun is absent (the state before layer-krun lands),
	// this is the visible skip — otherwise the launch would fail deep inside
	// containerd with an unhelpful "runtime handler not found".
	if _, lookErr := exec.LookPath(cap.Binary); lookErr != nil {
		return "", cap, deploykit.ContainerdTarget{}, fmt.Errorf(
			"vmbackend:krun: engine %q is selected but its CLI %q is not on PATH — install the containerd host layer (nerdctl/containerd)", engine, cap.Binary)
	}
	if !krunHandlerRuntimeInstalled() {
		return "", cap, deploykit.ContainerdTarget{}, fmt.Errorf(
			"vmbackend:krun: engine %q declares the %q handler but the handler's runtime (crun built with libkrun) is NOT installed on this host — "+
				"install the krun host layer (crun+libkrun+libkrunfw) before using backend: krun",
			engine, krunRuntimeHandler)
	}
	target, err := deploykit.ResolveContainerdTarget(spec.EngineName(engine), deploykit.Selection{}, rt)
	if err != nil {
		return "", cap, deploykit.ContainerdTarget{}, fmt.Errorf("vmbackend:krun: resolving the containerd target: %w", err)
	}
	return engine, cap, target, nil
}
