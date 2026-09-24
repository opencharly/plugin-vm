package vm

import (
	"os"
	"testing"
)

// TestBuildIsoVM_ConsoleModeLive exercises the CONSOLE branch end to end against
// the real Omarchy ISO: a spec with NO source.installer builds a BLANK disk and
// returns NO seed path (the medium boots its own interactive installer, driven
// over the console).
//
// It is gated on an EXPLICIT operator assertion — CHARLY_TEST_OMARCHY_ISO_CACHED=1
// — rather than probing charly's cache layout itself. Mirroring the SDK's cache
// key would make the test silently SKIP (a false green) the day the layout
// changes; instead the operator asserts the ISO is cached, and the test uses the
// REAL kit.FetchArtifact, which hits the cache. Unset → skip, reported visibly.
func TestBuildIsoVM_ConsoleModeLive(t *testing.T) {
	if os.Getenv("CHARLY_TEST_OMARCHY_ISO_CACHED") != "1" {
		t.Skip("set CHARLY_TEST_OMARCHY_ISO_CACHED=1 to run the live console build against the cached Omarchy ISO")
	}

	s := &VmSpec{}
	s.Source.Kind = "iso"
	s.Source.Distro = "omarchy"
	s.Source.URL = "https://iso.omarchy.org/omarchy-4.0.4.iso"
	s.Source.Installer = nil // CONSOLE mode: no answers volume
	s.DiskSize = "40G"

	out, err := BuildIsoVM(s, t.TempDir(), t.TempDir(), nil, &DistroDef{}, false)
	if err != nil {
		t.Fatalf("console-mode BuildIsoVM: %v", err)
	}
	if out.SeedIsoPath != "" {
		t.Fatalf("console mode must return NO seed path, got %q", out.SeedIsoPath)
	}
	if out.DiskPath == "" {
		t.Fatal("console mode must return a blank disk path")
	}
	if fi, err := os.Stat(out.DiskPath); err != nil || fi.Size() == 0 {
		t.Fatalf("console mode must create a non-empty blank disk at %s: %v", out.DiskPath, err)
	}
	if out.InstallerIsoRef == "" {
		t.Fatal("console mode must still reference the installer ISO")
	}
}
