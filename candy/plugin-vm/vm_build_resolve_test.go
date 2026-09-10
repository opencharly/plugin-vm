package vm

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// TestVmBuildDeployFromHop gates the vm-build deploy-from hop SEAM (Phase 3): a from:
// name:tag DRIVE target names the clone-base BED (a deploy whose own from: names the
// terminal kind:vm template) — the ONE chain resolver (loaderkit.DeployTargetEntity, the
// same seam the vm-build step threads) must resolve the terminal template. The seam is
// the testable unit; the wiring (resolveVmBuildViaDeployFrom calling it) is thin and
// covered by the live vm-build path.
func TestVmBuildDeployFromHop(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"check-vm-clone-base": {From: "cachyos-vm"},
		},
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"cachyos-vm": json.RawMessage("{}")},
		},
	}
	target, ok := loaderkit.DeployTargetEntity(uf, "check-vm-clone-base")
	if !ok || target != "cachyos-vm" {
		t.Fatalf("DeployTargetEntity(check-vm-clone-base) = (%q, %v), want (cachyos-vm, true) — the terminal template after the hop", target, ok)
	}
}

// TestVmBuildDeployFromHop_Qualified gates the NAMESPACE-QUALIFIED hop (the
// git-linked import form): a bed cloning from omarchy.check-charly-omarchy-vm
// (a kind:check bed in the imported namespace) must resolve to the qualified
// terminal template omarchy.omarchy-vm. This test FAILS without the
// namespace-aware DeployTargetEntity (sdk #247) — the runtime counterpart of
// the load-time ResolveEntityRef.
func TestVmBuildDeployFromHop_Qualified(t *testing.T) {
	ns := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"check-charly-omarchy-vm": {From: "omarchy-vm"},
		},
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"omarchy-vm": json.RawMessage("{}")},
		},
	}
	uf := &spec.UnifiedFile{
		Namespaces: map[string]*spec.UnifiedFile{"omarchy": ns},
	}
	target, ok := loaderkit.DeployTargetEntity(uf, "omarchy.check-charly-omarchy-vm")
	if !ok || target != "omarchy.omarchy-vm" {
		t.Fatalf("DeployTargetEntity(omarchy.check-charly-omarchy-vm) = (%q, %v), want (omarchy.omarchy-vm, true) — the qualified terminal template after the hop", target, ok)
	}
	// The unqualified form stays local-only (the no-leak contract).
	if _, ok := loaderkit.DeployTargetEntity(uf, "check-charly-omarchy-vm"); ok {
		t.Fatal("unqualified namespace bed leaked into the local scope")
	}
}
