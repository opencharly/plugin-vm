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
		Fleet: map[string]spec.FleetNode{
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
