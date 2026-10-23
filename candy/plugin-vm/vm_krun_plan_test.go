package vm

import (
	"slices"
	"strings"
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

// vm_krun_plan_test.go — R7 for the krun mapping. Every assertion here fails
// when its mapping line is removed; the comments name the mutation each one is
// designed to catch.

func krunFixture(krun *spec.VmKrun) *VmSpec {
	return &VmSpec{
		Ram:    "4G",
		Cpus:   3,
		Source: spec.VmSource{Kind: "bootc", Box: "test-box"},
		Krun:   krun,
	}
}

func krunTestInput(s *VmSpec, sshPort int) krunPlanInput {
	return krunPlanInput{
		Name:    "charly-test",
		Image:   "ghcr.io/opencharly/test-box:1",
		Engine:  "containerd",
		Target:  deploykit.ContainerdTarget{Engine: "containerd"},
		Spec:    s,
		SSHPort: sshPort,
		Systemd: true,
	}
}

func annotationValue(plan krunPlan, key string) (string, bool) {
	for _, a := range plan.Annotations {
		if a.Key == key {
			return a.Value, true
		}
	}
	return "", false
}

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// TestKrunPlanRAMSplit is the mapping's central claim: the guest RAM is NOT the
// container limit. Mutation: deleting the `krun.ram_mib` annotation, or setting
// it to the container limit, fails; deleting --memory/--memory-swap fails.
func TestKrunPlanRAMSplit(t *testing.T) {
	plan, err := buildKrunPlan(krunTestInput(krunFixture(nil), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if plan.ContainerMiB != 4096 {
		t.Fatalf("container limit = %d MiB, want 4096 (the authored ram)", plan.ContainerMiB)
	}
	if plan.GuestMiB != 4096-krunDefaultRAMHeadroomMiB {
		t.Fatalf("guest RAM = %d MiB, want 4096-%d (ram - headroom)", plan.GuestMiB, krunDefaultRAMHeadroomMiB)
	}
	if v, ok := annotationValue(plan, "krun.ram_mib"); !ok || v != "3840" {
		t.Fatalf("krun.ram_mib = (%q, %v), want 3840 (4096-256)", v, ok)
	}
	mem, ok := argValue(plan.ExtraArgs, "--memory")
	if !ok || mem != "4096m" {
		t.Fatalf("--memory = (%q, %v), want 4096m", mem, ok)
	}
	swap, ok := argValue(plan.ExtraArgs, "--memory-swap")
	if !ok || swap != "4096m" {
		t.Fatalf("--memory-swap = (%q, %v), want 4096m (swap disabled)", swap, ok)
	}
}

// TestKrunPlanHonoursAuthoredHeadroom pins that the headroom is the authored
// value, not the default: ram 4G + headroom 512 -> guest 3584.
func TestKrunPlanHonoursAuthoredHeadroom(t *testing.T) {
	specKrun := &spec.VmKrun{Network: "tsi", Kernel: "stock", GPU: "off", GPUFlags: 962, RAMHeadroomMiB: 512}
	plan, err := buildKrunPlan(krunTestInput(krunFixture(specKrun), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if plan.GuestMiB != 3584 {
		t.Fatalf("guest RAM = %d, want 3584 (4096-512)", plan.GuestMiB)
	}
	if v, _ := annotationValue(plan, "krun.ram_mib"); v != "3584" {
		t.Fatalf("krun.ram_mib = %q, want 3584", v)
	}
}

// TestKrunPlanCPUsIsAnnotationNotFlag pins the brief's "cpu -> krun.cpus (--cpus
// sets no vCPUs)". Mutation: swapping the annotation for `--cpus` fails BOTH the
// annotation assertion and the absence check.
func TestKrunPlanCPUsIsAnnotationNotFlag(t *testing.T) {
	plan, err := buildKrunPlan(krunTestInput(krunFixture(nil), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if v, ok := annotationValue(plan, "krun.cpus"); !ok || v != "3" {
		t.Fatalf("krun.cpus = (%q, %v), want 3", v, ok)
	}
	if slices.Contains(plan.ExtraArgs, "--cpus") {
		t.Fatalf("--cpus must NOT be passed (it sets no vCPUs): %v", plan.ExtraArgs)
	}
	req := krunStartPlanRequest(plan)
	if slices.Contains(req.ExtraArgs, "--cpus") {
		t.Fatalf("start-plan extra_args carries --cpus: %v", req.ExtraArgs)
	}
}

// TestKrunPlanPasstAndKernel pins the two enum-gated annotations and their
// ABSENCE for the defaults. Mutation: dropping either condition fails the
// positive arm; emitting them unconditionally fails the negative arm.
func TestKrunPlanPasstAndKernel(t *testing.T) {
	on := &spec.VmKrun{Network: "passt", Kernel: "image", GPU: "off", GPUFlags: 962, RAMHeadroomMiB: 256}
	plan, err := buildKrunPlan(krunTestInput(krunFixture(on), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if v, ok := annotationValue(plan, "krun.use_passt"); !ok || v != "1" {
		t.Fatalf("krun.use_passt = (%q, %v), want 1", v, ok)
	}
	if v, ok := annotationValue(plan, "krun.custom_kernel"); !ok || v != "1" {
		t.Fatalf("krun.custom_kernel = (%q, %v), want 1", v, ok)
	}

	off, err := buildKrunPlan(krunTestInput(krunFixture(nil), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan(defaults): %v", err)
	}
	for _, key := range []string{"krun.use_passt", "krun.custom_kernel"} {
		if _, ok := annotationValue(off, key); ok {
			t.Fatalf("%s emitted for the default settings", key)
		}
	}
}

// TestKrunPlanSystemd pins KRUN_INIT_PID1 + the nofile ulimit for a systemd
// guest, and their ABSENCE otherwise. Mutation: removing either fails the
// positive arm; emitting them for a non-systemd guest fails the negative arm.
func TestKrunPlanSystemd(t *testing.T) {
	in := krunTestInput(krunFixture(nil), 2222)
	plan, err := buildKrunPlan(in)
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if plan.Env["KRUN_INIT_PID1"] != "1" {
		t.Fatalf("KRUN_INIT_PID1 = %q, want 1", plan.Env["KRUN_INIT_PID1"])
	}
	if v, ok := argValue(plan.ExtraArgs, "--ulimit"); !ok || v != krunSystemdNoFileUlimit {
		t.Fatalf("--ulimit = (%q, %v), want %q", v, ok, krunSystemdNoFileUlimit)
	}

	in.Systemd = false
	nonSystemd, err := buildKrunPlan(in)
	if err != nil {
		t.Fatalf("buildKrunPlan(non-systemd): %v", err)
	}
	if _, ok := nonSystemd.Env["KRUN_INIT_PID1"]; ok {
		t.Fatal("KRUN_INIT_PID1 emitted for a non-systemd guest")
	}
	if slices.Contains(nonSystemd.ExtraArgs, "--ulimit") {
		t.Fatal("--ulimit emitted for a non-systemd guest")
	}
}

// TestKrunPlanSSHLoopback pins the published loopback port. Mutation: dropping
// the port fails; publishing on a wildcard address fails.
func TestKrunPlanSSHLoopback(t *testing.T) {
	plan, err := buildKrunPlan(krunTestInput(krunFixture(nil), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if len(plan.Ports) != 1 || plan.Ports[0] != "127.0.0.1:2222:22" {
		t.Fatalf("ports = %v, want [127.0.0.1:2222:22]", plan.Ports)
	}
}

// TestKrunPlanRefusals pins the two measured refusals and the headroom
// sanity check. Mutation: removing any refusal makes the corresponding arm pass
// with an empty error.
func TestKrunPlanRefusals(t *testing.T) {
	in := krunTestInput(krunFixture(nil), 2222)
	in.NVIDIA = true
	if _, err := buildKrunPlan(in); err == nil || !strings.Contains(err.Error(), "NEEDS UPSTREAM") {
		t.Fatalf("krun + NVIDIA = %v, want a NEEDS UPSTREAM refusal", err)
	}

	// 1G of authored RAM with the default 256 MiB headroom is 768 MiB of guest
	// RAM — below the 1024 MiB Vulkan-compute floor.
	gpuSpec := &spec.VmKrun{Network: "tsi", Kernel: "stock", GPU: "virtio", GPUFlags: 962, RAMHeadroomMiB: 256}
	in = krunTestInput(krunFixture(gpuSpec), 2222)
	in.Spec.Ram = "1G"
	if _, err := buildKrunPlan(in); err == nil || !strings.Contains(err.Error(), "1024 MiB") {
		t.Fatalf("krun GPU below the memory floor = %v, want the 1024 MiB refusal", err)
	}

	// A headroom at or above the authored RAM leaves no guest RAM at all.
	in = krunTestInput(krunFixture(nil), 2222)
	in.Spec.Krun = &spec.VmKrun{Network: "tsi", Kernel: "stock", GPU: "off", GPUFlags: 962, RAMHeadroomMiB: 4096}
	if _, err := buildKrunPlan(in); err == nil || !strings.Contains(err.Error(), "host headroom") {
		t.Fatalf("headroom >= ram = %v, want the headroom refusal", err)
	}
}

// TestKrunApplyTarget pins the global-flag placement: the privilege prefix
// precedes the binary, and --address/--namespace sit between the binary and the
// subcommand (nerdctl rejects global flags after `run`).
func TestKrunApplyTarget(t *testing.T) {
	got := krunApplyTarget(
		[]string{"nerdctl", "run", "-d", "img"},
		deploykit.ContainerdTarget{Address: "/run/containerd/containerd.sock", Namespace: "default", PrivilegePrefix: "sudo -n"},
	)
	want := []string{"sudo", "-n", "nerdctl", "--address", "/run/containerd/containerd.sock", "--namespace", "default", "run", "-d", "img"}
	if !slices.Equal(got, want) {
		t.Fatalf("krunApplyTarget = %v, want %v", got, want)
	}
}

// TestKrunStartArgvThroughTheEngineClass exercises the REAL engine start-plan
// servant (spec/container.InvokeEngineOp): the mapping's argv is what the
// containerd capability actually produces. It proves --runtime krun is carried,
// the memory/annotation args survive, --cpus is absent, and the image is last.
func TestKrunStartArgvThroughTheEngineClass(t *testing.T) {
	plan, err := buildKrunPlan(krunTestInput(krunFixture(nil), 2222))
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	argv, err := krunStartArgv(plan, deploykit.ContainerdTarget{Address: "/run/containerd/containerd.sock", Namespace: "default"})
	if err != nil {
		t.Fatalf("krunStartArgv: %v", err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--runtime krun",
		"--memory 4096m",
		"--memory-swap 4096m",
		"--annotation krun.ram_mib=3840",
		"--annotation krun.cpus=3",
		"-p 127.0.0.1:2222:22",
		"ghcr.io/opencharly/test-box:1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v does not contain %q", argv, want)
		}
	}
	if strings.Contains(joined, "--cpus") {
		t.Fatalf("argv carries --cpus: %v", argv)
	}
	if argv[len(argv)-1] != "ghcr.io/opencharly/test-box:1" {
		t.Fatalf("image is not last in %v", argv)
	}
}

// TestKrunPlanPortForwards pins that authored network.port_forwards are
// published on loopback alongside SSH. Mutation: dropping the forward loop
// silently ignores a documented field.
func TestKrunPlanPortForwards(t *testing.T) {
	in := krunTestInput(krunFixture(nil), 2222)
	in.PortForwards = []string{"8080:80"}
	plan, err := buildKrunPlan(in)
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	want := []string{"127.0.0.1:2222:22", "127.0.0.1:8080:80"}
	if !slices.Equal(plan.Ports, want) {
		t.Fatalf("ports = %v, want %v", plan.Ports, want)
	}
}

// TestKrunPlanSSHPubKey pins the per-domain key's host-side handoff: a krun
// guest has no cloud-init seed, so the public key rides the plan env. Mutation:
// dropping the env leaves the guest with no authorization source.
func TestKrunPlanSSHPubKey(t *testing.T) {
	in := krunTestInput(krunFixture(nil), 2222)
	in.SSHPubKey = "ssh-ed25519 AAAATEST w34"
	plan, err := buildKrunPlan(in)
	if err != nil {
		t.Fatalf("buildKrunPlan: %v", err)
	}
	if got := plan.Env["KRUN_SSH_PUBKEY"]; got != in.SSHPubKey {
		t.Fatalf("KRUN_SSH_PUBKEY = %q, want the per-domain public key", got)
	}
}
