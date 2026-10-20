package vm

import (
	"os"
	"strings"
	"testing"
)

// TestAutoBuildCallSiteCarriesTheProjectDir pins the CALL SITE, not just the
// helper: ensureBuilderImageBuilt must resolve the project dir (os.Getwd — the
// plugin's cwd is the project, the convention every other path resolver in this
// plugin uses) and build its request through builderAutoBuildRequest. The old
// code marshalled an inline literal with no Dir at all, which the drive reported
// as "buildkitdrive: build: no context".
func TestAutoBuildCallSiteCarriesTheProjectDir(t *testing.T) {
	src, err := os.ReadFile("vm_build_resolve.go")
	if err != nil {
		t.Fatalf("reading vm_build_resolve.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"dir, werr := os.Getwd()",
		"builderAutoBuildRequest(builderRef, dir)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("vm_build_resolve.go is missing %q — the auto-build call site must carry the project dir", want)
		}
	}
}
