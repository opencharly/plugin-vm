package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildIsoVM_ConsoleModeLive exercises the CONSOLE branch end to end against
// the real Omarchy ISO: a spec with NO source.installer builds a BLANK disk and
// returns NO seed path (the medium boots its own interactive installer, driven
// over the console). It SKIPS when the ISO is not in charly's content-addressed
// cache — the download is 5.8 GiB — so a normal `go test` stays hermetic and the
// skip is reported visibly rather than the boundary being faked.
func TestBuildIsoVM_ConsoleModeLive(t *testing.T) {
	const url = "https://iso.omarchy.org/omarchy-4.0.4.iso"
	if !isoCached(url) {
		t.Skip("omarchy-4.0.4.iso not in charly's vm-image cache; skipping the live console build")
	}

	s := &VmSpec{}
	s.Source.Kind = "iso"
	s.Source.Distro = "omarchy"
	s.Source.URL = url
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

// isoCached reports whether the ISO for url is already in charly's vm-image
// cache. The cache layout is the SDK's (content-addressed by sha256(url), under
// ~/.cache/charly/vm-images), mirrored here ONLY to decide a test skip — a miss
// would otherwise trigger a 5.8 GiB download inside `go test`.
func isoCached(url string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	h := sha256.Sum256([]byte(url))
	p := filepath.Join(home, ".cache", "charly", "vm-images", hex.EncodeToString(h[:])+".iso")
	fi, err := os.Stat(p)
	return err == nil && fi.Size() > 0
}
