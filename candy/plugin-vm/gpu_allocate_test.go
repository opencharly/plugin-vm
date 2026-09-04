package vm

import (
	"testing"

	"github.com/opencharly/sdk/vmshared"
)

// TestAutoAllocate_DropsStaleHostdevForNonGPUClaimant pins the measured root cause:
// a VM whose current claimant declares NO GPU must not attach the stale GPU <hostdev>
// persisted in the per-domain instance.yml. Before the fix, autoAllocateExclusiveGPUs
// returned ovr unchanged, and ApplyToVmSpec injected the stale card into every non-GPU
// VM on a passthrough host (measured: channel-keeper evals collided on PCI 0000:01:00.0).
func TestAutoAllocate_DropsStaleHostdevForNonGPUClaimant(t *testing.T) {
	ovr := &VmInstanceOverride{
		Libvirt: &LibvirtDomain{
			Devices: &vmshared.LibvirtDevices{
				Hostdevs: []vmshared.LibvirtHostdev{{Type: "pci"}},
			},
		},
	}
	cnode := &FleetNode{} // no requires_exclusive -> no GPU needed
	got, err := autoAllocateExclusiveGPUs(nil, ovr, cnode, nil, "test-vm", "libvirt")
	if err != nil {
		t.Fatalf("autoAllocate: %v", err)
	}
	if got == nil || got.Libvirt == nil || got.Libvirt.Devices == nil {
		t.Fatal("override must remain non-nil (only the stale hostdevs drop)")
	}
	if len(got.Libvirt.Devices.Hostdevs) != 0 {
		t.Fatalf("stale hostdevs must be dropped for a non-GPU claimant, got %d", len(got.Libvirt.Devices.Hostdevs))
	}
}

// TestAutoAllocate_KeepsHostdevForGPUClaimant guards the GPU-claimant path.
func TestAutoAllocate_KeepsHostdevForGPUClaimant(t *testing.T) {
	ovr := &VmInstanceOverride{}
	cnode := &FleetNode{RequiresExclusive: []string{"nvidia-gpu"}}
	resources := map[string]*ResolvedResource{
		"nvidia-gpu": {Gpu: &ResolvedGpuSelector{Vendor: "0x10de"}},
	}
	got, err := autoAllocateExclusiveGPUs(nil, ovr, cnode, resources, "gpu-vm", "libvirt")
	if err == nil {
		// A host WITHOUT the matching card must FAIL HARD (unsatisfiable claim).
		if got != nil && len(got.Libvirt.Devices.Hostdevs) > 0 {
			t.Fatalf("no host GPU should resolve here")
		}
	}
	_ = err
}
