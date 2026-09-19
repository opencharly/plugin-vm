package vm

import (
	"github.com/opencharly/spec/deploy"
	"github.com/opencharly/spec/spec"
)

// vm_shape_override.go — the per-deploy VM-shape override (ram/cpu), the capability that
// lets MANY deploys share ONE kind:vm template while each boots at its own size. It is the
// reader of the `ram:`/`cpu:` fields #Deploy carries (spec/schema/deploy.cue `cpu?`/`ram?`) —
// authorable on a `from:` VM deploy before this, but no VM code read them, so every deploy
// was pinned to the template's shape. That forced a consumer with a different sizing need (a
// lean 16-lane eval clone vs the full keeper) to duplicate the WHOLE template in its own repo
// (R3) instead of deriving. The VM-shape spellings now match `#Vm`'s own (`cpu:`/`ram:`) —
// the deploy arm was the plural outlier `cpus:` until this cutover — and the reader landed
// alongside that alignment.
//
// Semantics — INHERITANCE along the `from:` chain, nearest-wins:
//
//	A deploy deriving from another deploy deriving from the template inherits the nearest
//	non-empty shape up the chain; the terminal kind:vm template is the floor. So a clone bed
//	(`from: <golden-base>:golden`) that declares nothing inherits the golden base's shape,
//	and only the provisioning root states it — ONE declaration per consumer, never one per
//	derived bed (R3). This mirrors how the container side inherits through `from:`.
//
// Only cpu/ram are overridable, and #Deploy carries no disk_size at all for this reason: the
// base disk is built ONCE per ENTITY and shared read-only by every deploy (per-domain COW
// overlays), so a per-deploy disk size could not resize an already-built disk.

// vmShapeOverride returns the effective (ram, cpus) for a VM deploy, walking its from:
// chain from the deploy being created up to (not including) the terminal template. entity
// is the kind:vm entity positional the create was invoked with (the disk/spec SOURCE);
// identity is the per-deploy DOMAIN identity (the --domain flag value) — the deploy node
// actually being created, which may be a clone bed whose from: is a base bed, not the
// template. Nearest non-empty (ram, cpus) wins; ("",0) when nothing in the chain declares
// one, so the caller keeps the template's own shape.
func vmShapeOverride(uf *spec.UnifiedFile, entity, identity string) (string, int) {
	if uf == nil || len(uf.Deploy) == 0 {
		return "", 0
	}
	node, ok := lookupDeployByIdentity(uf.Deploy, identity)
	if !ok {
		// No --domain (a direct `charly vm create`): the positional names the entity or a
		// deploy directly. Either way, a deploy node keyed by it is the start.
		node, ok = uf.Deploy[entity]
	}
	if !ok {
		return "", 0
	}
	// Walk the from: chain — bounded so a malformed cycle cannot spin. The terminal
	// kind:vm template has no Deploy entry, so the walk ends there.
	for i := 0; i < 16; i++ {
		if node.Ram != "" || node.Cpus > 0 {
			return string(node.Ram), node.Cpus
		}
		if node.From == "" {
			return "", 0
		}
		next, ok := uf.Deploy[node.From]
		if !ok {
			return "", 0
		}
		node = next
	}
	return "", 0
}

// lookupDeployByIdentity finds the deploy node whose key matches identity after
// VmDomainIdentity normalization (the SAME identity space FindVMClaimant scopes by: the
// domain is named by the DEPLOY, but legacy keys may carry a `vm:` prefix or a slash
// instance that normalization strips).
func lookupDeployByIdentity(tree map[string]spec.DeployNode, identity string) (spec.DeployNode, bool) {
	if identity == "" {
		return spec.DeployNode{}, false
	}
	if n, ok := tree[identity]; ok {
		return n, true
	}
	want := spec.VmDomainIdentity(identity)
	for name, n := range tree {
		if deploy.IsVmVenue(&n) && spec.VmDomainIdentity(name) == want {
			return n, true
		}
	}
	return spec.DeployNode{}, false
}

// applyVmShapeOverride folds the resolved chain override onto a template-resolved VM spec,
// leaving the template value in place for whichever field the chain did not state. A no-op
// ("",0) leaves dst untouched, so a deploy with no override boots exactly the template.
// (dst, not spec: the file imports github.com/opencharly/spec/spec, and a `spec` parameter
// would shadow that package.)
func applyVmShapeOverride(dst *VmSpec, ram string, cpus int) {
	if dst == nil {
		return
	}
	if ram != "" {
		dst.Ram = ram
	}
	if cpus > 0 {
		dst.Cpus = cpus
	}
}
