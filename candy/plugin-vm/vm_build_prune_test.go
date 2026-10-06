package vm

import "testing"

// TestPruneAfterVmBuildNoReverseChannelIsNoOp is the regression guard for the wiring:
// pruneAfterVmBuild is BEST-EFFORT and must be a clean no-op when there is no host
// reverse channel (the out-of-process placement), never a panic — the disk build is the
// primary artifact and the box prune is its metadata-wrapper cleanup
// (opencharly/charly#808).
func TestPruneAfterVmBuildNoReverseChannelIsNoOp(t *testing.T) {
	oldCtx, oldExec := cmdCtx, cmdExec
	t.Cleanup(func() { cmdCtx, cmdExec = oldCtx, oldExec })
	cmdExec = nil // no reverse channel
	pruneAfterVmBuild()
}
