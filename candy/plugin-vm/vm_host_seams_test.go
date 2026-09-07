package vm

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// TestVmHostSeamsDeployHop gates the create's config-resolve deploy-hop SEAM (Phase 3):
// the requested name may be the BASE BED (the clone-base deploy) whose from: names the
// terminal template — the ONE chain resolver (loaderkit.DeployTargetEntity, the same seam
// the host-seams config-resolve threads) must resolve the terminal template. The seam is
// the testable unit; the wiring (the host-seams config-resolve calling it) is thin and
// covered by the live create path.
func TestVmHostSeamsDeployHop(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
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
