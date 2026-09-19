package vm

import (
	"context"
	"fmt"
	"os"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/deploy"
	"github.com/opencharly/spec/spec"
)

// vm_host_seams.go — the command:vm plugin's bridge to the host. The VM CLI handlers moved out of
// charly core (P10); the config loader + runtime-settings store + deploy ledger + egress subsystem
// are core Mechanisms a plugin cannot import (separate module), so the handlers reach them over the
// in-proc reverse channel: config → HostBuild("config-resolve"), ledger writes plugin-side
// (candy/plugin-vm/vm_host_persist.go — the former HostBuild("config-persist") is DELETED),
// egress → InvokeProvider(verb:egress). command:vm is COMPILED-IN and dispatches
// exactly ONE `charly vm …` invocation per process, so the reverse-channel executor is stashed in a
// package var at Invoke(OpRun) entry (setCommandContext) — race-free single-command-per-process.

// Spec-type aliases the moved handlers reference by their core (package main) short names. All are
// canonical sdk/spec wire types (the same identity core used via its own alias surface).
type (
	DeployNode          = spec.Deploy
	ResolvedResource    = spec.ResolvedResource
	ResolvedGpuSelector = spec.ResolvedGpuSelector
	VFIOReport          = spec.VFIOReport
	VFIOPCIDevice       = spec.VFIOPCIDevice
)

// cmdCtx / cmdExec carry the Invoke(OpRun) reverse-channel handle to the deep CLI call sites.
var (
	cmdCtx  context.Context
	cmdExec *sdk.Executor
)

// setCommandContext stashes the reverse-channel executor for the duration of one `charly vm …`
// dispatch. Called once at the top of command:vm's Invoke(OpRun).
func setCommandContext(ctx context.Context, ex *sdk.Executor) {
	cmdCtx = ctx
	cmdExec = ex
}

// resolvedConfig is the plugin-facing result of hostConfigResolve. Since K-wave 2 cone R2 bank D
// (the "config-resolve" HostBuild seam DELETED), hostConfigResolve computes every field
// PLUGIN-SIDE — the kind:vm entity resolves to *spec.ResolvedVm directly (vmshared.VmSpec IS
// spec.ResolvedVm, so the former VmJSON envelope decode was identity), the resources via
// spec.ResolvePluginKindViaPlugin over loaderkit.ResolveResourceViaExecutor, the runtime settings
// via kit.ResolveRuntime, and VmState via loaderkit.ResolveVmStateViaExecutor.
// Claimant/ClaimantNode are computed PLUGIN-SIDE (deploykit.MergedDeployTree + deploy.FindVMClaimant
// over the plugin's loader-backed reader).
type resolvedConfig struct {
	VM           *VmSpec
	Resources    map[string]*ResolvedResource
	Backend      string
	Claimant     string
	ClaimantNode *spec.Deploy
	VmBackend    string
	BuildEngine  string
	RunEngine    string
	VmState      *spec.VmDeployState
	VmEntities   []string
}

// hostConfigResolve resolves the project config for an entity PLUGIN-SIDE (K-wave 2 cone R2 bank D
// — the "config-resolve" HostBuild seam is DELETED): the runtime settings come from
// kit.ResolveRuntime (this plugin is compiled-in, sharing charly's process + runtime config), the
// project loads via loaderkit.LoadUnifiedViaExecutor, the kind:vm entity resolves via
// loaderkit.ResolveVmEntityViaExecutor + loaderkit.ApplyCueDefaults (the schema-declared defaults
// the former host seam applied), the resources via spec.ResolvePluginKindViaPlugin over
// loaderkit.ResolveResourceViaExecutor, and the persisted VmState via
// loaderkit.ResolveVmStateViaExecutor. The exclusive-resource Claimant is computed PLUGIN-SIDE
// (#55 coneC-dsh β2 config-RESOLVE) from the loaded project deploy via deploykit.MergedDeployTree +
// vmClaimant; the effective VM backend is computed HERE (resolveVmBackendPlugin/
// vmConfiguredBackendPlugin, F6 vm-lifecycle move, vm_backend_resolve.go).
//
// claimantID is the DOMAIN IDENTITY of the deploy requesting this resolve — REQUIRED for every
// caller that reads cfg.Claimant (the create/stop/destroy paths, all of which carry the deploy's
// --domain). It is threaded into vmClaimant so the claimant resolves to THIS deploy's node, never
// an arbitrary sibling sharing the same `from:` entity (RCA: 16 beds all `from: omarchy-vm`, one
// carrying requires_exclusive: [nvidia-gpu]; an identity-less scan made every sibling demand the
// GPU). Pass "" ONLY when the caller reads NO claimant field — the config-only readers
// (Resources/VmState/Backend/Vm/VMEntities) and the direct `charly vm create <entity>` with no
// --domain, which spec documents as an entity-wide scan (ambiguous → no claim). Identity is an
// explicit argument at every call site, never a hidden fallback.
func hostConfigResolve(entity, claimantID string) (resolvedConfig, error) {
	if cmdExec == nil {
		return resolvedConfig{}, fmt.Errorf("config-resolve: no host reverse channel (command not compiled-in?)")
	}
	rt, err := kit.ResolveRuntime()
	if err != nil {
		return resolvedConfig{}, err
	}
	dir, _ := os.Getwd()
	cfg := resolvedConfig{
		VmBackend:   rt.VmBackend,
		BuildEngine: rt.BuildEngine,
		RunEngine:   rt.RunEngine,
	}
	uf, ok, err := loaderkit.LoadUnifiedViaExecutor(cmdCtx, cmdExec, dir)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("config-resolve: load project: %w", err)
	}
	if ok && uf != nil {
		// The kind:vm entity names + the requested entity's resolved spec (vmshared.VmSpec IS
		// spec.ResolvedVm, so no wire envelope decode — the former VmJSON round-trip was identity).
		for name := range uf.VM() {
			cfg.VmEntities = append(cfg.VmEntities, name)
		}
		if entity != "" {
			// The from: name:tag deploy-hop (Phase 3): the requested name may be the BASE BED
			// (the clone-base deploy) whose from: names the terminal template — the ONE
			// chain resolver (loaderkit.DeployTargetEntity) handles the plain-entity and
			// deploy-hop cases alike. The DISK/domain keying below stays on c.Box (the
			// requested name — where the vm-build drive wrote <vm.image_dir>/<box>/); only the
			// SPEC resolve follows the chain.
			target, ok := loaderkit.DeployTargetEntity(uf, entity)
			if ok {
				vm, verr := loaderkit.ResolveVmEntityViaExecutor(cmdCtx, cmdExec, dir, target)
				if verr != nil {
					return resolvedConfig{}, verr
				}
				// ApplyCueDefaults fills schema-declared defaults. Order-independent vs the
				// plugin's instance-override / GPU-alloc merge (those touch ONLY libvirt overlays).
				// The opaque substrate-template echo (Raw) is cleared for the closed-schema unify
				// round-trip and restored on the value the plugin receives.
				if vm != nil {
					savedRaw := vm.Raw
					vm.Raw = nil
					if derr := loaderkit.ApplyCueDefaults("vm", vm); derr != nil {
						return resolvedConfig{}, fmt.Errorf("applying vm defaults for %q: %w", entity, derr)
					}
					vm.Raw = savedRaw
					// Per-deploy VM-shape override (ram/cpu): the deploy node's own
					// fields, inherited along its from: chain, over the template's —
					// the capability that lets many deploys share ONE template at
					// different sizes instead of duplicating it (R3). A no-op when
					// nothing in the chain declares a shape.
					ovrRam, ovrCpus := vmShapeOverride(uf, entity, claimantID)
					applyVmShapeOverride(vm, ovrRam, ovrCpus)
					cfg.VM = vm
				}
			}
		}
		cfg.Resources = spec.ResolvePluginKindViaPlugin(uf, "resource", loaderkit.ResolveResourceViaExecutor(cmdCtx, cmdExec))
		// Claimant computation moved plugin-side (#55 coneC-dsh β2 config-RESOLVE): merge the
		// per-host overlay (placement-invariant reader = loaderkit.LoadHostDeployConfigViaExecutor)
		// then resolve identity-scoped — the ONE seam the create/stop/destroy paths share.
		if claimant, claimantNode, hasClaimant := resolveClaimant(uf.Deploy, entity, claimantID, func() (*deploykit.DeployConfig, error) {
			return loaderkit.LoadHostDeployConfigViaExecutor(cmdCtx, cmdExec)
		}); hasClaimant {
			cfg.Claimant = claimant
			cfg.ClaimantNode = &claimantNode
		}
	}
	// The persisted deploy-ledger runtime state (READ half) — plugin-side (the former
	// config-resolve VmState leg).
	cfg.VmState, _ = loaderkit.ResolveVmStateViaExecutor(cmdCtx, cmdExec, entity)
	backend, err := resolveVmBackendPlugin(vmConfiguredBackendPlugin(cmdCtx, cmdExec, entity, cfg.VmBackend))
	if err != nil {
		return resolvedConfig{}, err
	}
	cfg.Backend = backend
	return cfg, nil
}

// resolveClaimant is the ONE exclusive-resource claimant resolver (R3): it merges the per-host
// deploy overlay onto the project tree via the placement-invariant reader, then binds the result
// to spec/deploy.FindVMClaimant — which is IDENTITY-SCOPED when claimantID is non-empty. Every
// caller that reads the claimant passes the deploy's DOMAIN IDENTITY (the --domain flag value),
// so THIS deploy's own node resolves, never an arbitrary sibling sharing the same `from:` entity.
//
// The merge and the identity-scope are one seam because the two bugs shared one shape: the
// caller must supply both the tree AND its own identity. Extracted (not a bare call) so the
// identity-scoping regression is unit-testable without the host reverse channel; the reader is a
// parameter so a test can supply a fixed overlay.
func resolveClaimant(project map[string]DeployNode, entity, claimantID string, read func() (*deploykit.DeployConfig, error)) (string, DeployNode, bool) {
	merged := deploykit.MergedDeployTree(project, "vm config-resolve", read)
	return deploy.FindVMClaimant(merged, entity, claimantID)
}

// hostConfigPersist now lives in vm_host_persist.go — the PLUGIN-SIDE deploy-ledger persist path
// (#55 coneC-dsh β2 config-PERSIST shed: the former HostBuild("config-persist") host-builder is
// deleted; the plugin calls deploykit.SaveVmDeployState/RemoveVmDeployEntry directly with its own
// lock + marshal + reader).
