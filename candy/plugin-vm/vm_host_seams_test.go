package vm

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// TestVmHostSeamsDeployHop gates the create's config-resolve deploy-hop (Phase 3): the
// requested name may be the BASE BED (the clone-base deploy) whose from: names the terminal
// template — the ONE chain resolver (loaderkit.DeployTargetEntity, the same seam the
// host-seams config-resolve threads) must resolve the terminal template. Removing the hop
// fails the bed case (the target would name the clone-base BED, not the template).
func TestVmHostSeamsDeployHop(t *testing.T) {
	uf := &spec.UnifiedFile{
		Fleet: map[string]spec.FleetNode{
			"check-omarchy-clone-base": {From: "omarchy-vm"},
		},
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"omarchy-vm": json.RawMessage("{}")},
		},
	}
	target, ok := loaderkit.DeployTargetEntity(uf, "check-omarchy-clone-base")
	if !ok || target != "omarchy-vm" {
		t.Fatalf("DeployTargetEntity(check-omarchy-clone-base) = (%q, %v), want (omarchy-vm, true) — the terminal template after the hop", target, ok)
	}
}
