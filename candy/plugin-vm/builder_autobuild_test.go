package vm

import "testing"

// TestBuilderAutoBuildRequestCarriesTheProjectDir pins plugin-vm#87: the VM
// auto-builder's build request must carry the project dir, or the drive fails
// with "buildkitdrive: build: no context" even though the same box builds
// cleanly through `charly box build`.
func TestBuilderAutoBuildRequestCarriesTheProjectDir(t *testing.T) {
	const dir = "/projects/distro-cachyos"
	req := builderAutoBuildRequest("cachyos-pacstrap-builder", dir)

	if req.Dir != dir {
		t.Fatalf("builderAutoBuildRequest(...).Dir = %q, want the project dir %q", req.Dir, dir)
	}
	if len(req.Boxes) != 1 || req.Boxes[0] != "cachyos-pacstrap-builder" {
		t.Fatalf("builderAutoBuildRequest(...).Boxes = %v, want [cachyos-pacstrap-builder]", req.Boxes)
	}
	if !req.IncludeDisabled {
		t.Fatal("builderAutoBuildRequest(...).IncludeDisabled = false, want true")
	}
}
