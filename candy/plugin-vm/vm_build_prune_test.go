package vm

import (
	"encoding/json"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestVmBoxPruneRequestIsBuildPrune pins the request the post-build prune actually issues:
// the BuildPrune retention scope (tag retention + .build staging only, matching
// plugin-box's pruneAfterBuild), the project dir, and the resolved keep_images. This is
// the behavior of the new prune — not merely that it no-ops — so the request that reclaims
// the VM box tags (opencharly/charly#808) is asserted, not the early return.
func TestVmBoxPruneRequestIsBuildPrune(t *testing.T) {
	raw, err := vmBoxPruneRequestJSON("/proj/dir", 3)
	if err != nil {
		t.Fatalf("vmBoxPruneRequestJSON: %v", err)
	}
	var req spec.RetentionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !req.BuildPrune {
		t.Errorf("the VM box prune must use the BuildPrune retention scope (matching plugin-box), got BuildPrune=false")
	}
	if req.Dir != "/proj/dir" {
		t.Errorf("retention dir = %q, want the project dir", req.Dir)
	}
	if req.KeepImages != 3 {
		t.Errorf("KeepImages = %d, want the resolved keep_images (3)", req.KeepImages)
	}
}

// TestPruneAfterVmBuildNoReverseChannelIsNoOp: the prune is BEST-EFFORT and must be a
// clean no-op with no host reverse channel (out-of-process placement) — never a panic.
func TestPruneAfterVmBuildNoReverseChannelIsNoOp(t *testing.T) {
	oldCtx, oldExec := cmdCtx, cmdExec
	t.Cleanup(func() { cmdCtx, cmdExec = oldCtx, oldExec })
	cmdExec = nil
	pruneAfterVmBuild()
}
