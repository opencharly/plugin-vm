package vm

import (
	"testing"
)

// TestShouldRepackIsoAnswersSkipsGoldenClone gates the golden-clone iso re-pack skip
// (B12): shouldRepackIsoAnswers with a golden-clone disk (isGoldenClone=true) + an iso
// source + perDomain + a seed path must return FALSE (the re-pack is skipped). Removing
// the && !isGoldenClone clause from shouldRepackIsoAnswers makes this case return TRUE
// -> this test FAILS.
func TestShouldRepackIsoAnswersSkipsGoldenClone(t *testing.T) {
	spec := &VmSpec{Source: VmSource{Kind: "iso"}}
	if shouldRepackIsoAnswers(spec, true, "/some/seed.iso", true) {
		t.Fatal("golden clone must SKIP the iso answers re-pack")
	}
	// Control: a plain (non-clone) disk must re-pack.
	if !shouldRepackIsoAnswers(spec, true, "/some/seed.iso", false) {
		t.Fatal("a plain disk must re-pack the iso answers")
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
