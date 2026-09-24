package vm

import (
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestShouldRepackIsoAnswersSkipsGoldenClone gates the golden-clone iso re-pack skip
// (B12): shouldRepackIsoAnswers with a golden-clone disk (isGoldenClone=true) + an iso
// source + perDomain + a seed path must return FALSE (the re-pack is skipped). Removing
// the && !isGoldenClone clause from shouldRepackIsoAnswers makes this case return TRUE
// -> this test FAILS.
func TestShouldRepackIsoAnswersSkipsGoldenClone(t *testing.T) {
	// An UNATTENDED iso spec (it authors source.installer) is the re-pack case.
	unattended := &VmSpec{Source: VmSource{Kind: "iso", Installer: &spec.VmInstaller{Password_hash: "$6$x$y"}}}
	if shouldRepackIsoAnswers(unattended, true, "/some/seed.iso", true) {
		t.Fatal("golden clone must SKIP the iso answers re-pack")
	}
	// Control: a plain (non-clone) disk must re-pack.
	if !shouldRepackIsoAnswers(unattended, true, "/some/seed.iso", false) {
		t.Fatal("a plain disk must re-pack the iso answers")
	}
	// A CONSOLE-mode iso spec (no source.installer) has NO answers volume, so the
	// re-pack must be SKIPPED — RepackPerDomainSeed would fail looking for a seed
	// sidecar the console build never wrote. Removing the isoConsoleMode gate makes
	// this return TRUE -> FAIL.
	con := &VmSpec{Source: VmSource{Kind: "iso"}}
	if shouldRepackIsoAnswers(con, true, "/some/seed.iso", false) {
		t.Fatal("a console-mode iso VM must SKIP the iso answers re-pack (no seed exists)")
	}
}

// TestNeedsPerDomainSeedGatesGoldenClone gates the golden-clone per-domain seed
// regeneration: a from:name:tag golden clone (the disk's backing file marks it) needs
// the per-domain seed even though the spec resolves to the iso entity — the clone is
// POST-INSTALL and must get the DOMAIN's key injection (the iso answers re-pack is
// skipped separately). Removing the || isGoldenClone clause from needsPerDomainSeed
// makes the golden-clone case return FALSE -> this test FAILS.
func TestNeedsPerDomainSeedGatesGoldenClone(t *testing.T) {
	iso := &VmSpec{Source: VmSource{Kind: "iso"}}
	if !needsPerDomainSeed(iso, true) {
		t.Fatal("a golden clone must regenerate the per-domain seed")
	}
	if needsPerDomainSeed(iso, false) {
		t.Fatal("a plain iso install must NOT regenerate the cloud-init seed (the answers re-pack covers it)")
	}
	if !needsPerDomainSeed(&VmSpec{Source: VmSource{Kind: "cloud_image"}}, false) {
		t.Fatal("cloud_image always regenerates the per-domain seed")
	}
	if !needsPerDomainSeed(&VmSpec{Source: VmSource{Kind: "clone"}}, false) {
		t.Fatal("clone always regenerates the per-domain seed")
	}
}
