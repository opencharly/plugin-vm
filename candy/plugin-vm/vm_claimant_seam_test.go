package vm

// vm_claimant_seam_test.go — the identity-scoped claimant-resolve seam.
//
// Regression (RCA): MANY deploys share one kind:vm entity (a 16-bed omarchy suite all
// `from: omarchy-vm`), and exactly one carries `requires_exclusive: [nvidia-gpu]`. An
// ENTITY-WIDE claimant scan made every sibling inherit that claim: `check-omarchy-iso-vm`,
// a GPU-free bed, failed `charly vm create` demanding an NVIDIA card present only on the
// hybrid-GPU bed's host. The fix threads the deploy's DOMAIN IDENTITY (the --domain value)
// into spec/deploy.FindVMClaimant so the resolve is scoped to THIS deploy.
//
// These tests pin the SEAM the create/stop/destroy paths share (resolveClaimant):
//   - create  → acquires the lease for the resolved claimant;
//   - stop/destroy → releases it, with the SAME identity (so a sibling's lease is never
//     touched and this deploy's is never leaked).
// They FAIL if resolveClaimant drops the identity (reverting to the entity-wide scan).

import (
	"testing"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/spec"
)

func claimantNode(from string, excl ...string) spec.DeployNode {
	n := spec.DeployNode{From: from}
	n.Descent = &spec.DescentDescriptor{Venue: "ssh", Transport: "ssh"}
	n.RequiresExclusive = excl
	return n
}

// noOverlay is the placement-invariant reader with no per-host overlay — the project tree
// stands alone, which is the case the bug surfaced in.
func noOverlay() (*deploykit.DeployConfig, error) { return nil, nil }

// The bug: an identity-less resolve must NOT hand a GPU-free sibling the hybrid bed's claim.
func TestResolveClaimant_IdentityScopedNotSibling(t *testing.T) {
	project := map[string]DeployNode{
		"check-omarchy-hybrid-gpu-vm": claimantNode("omarchy-vm", "nvidia-gpu"),
		"check-omarchy-iso-vm":        claimantNode("omarchy-vm"),
	}
	if name, _, ok := resolveClaimant(project, "omarchy-vm", "check-omarchy-iso-vm", noOverlay); ok {
		t.Fatalf("the GPU-free iso bed must resolve NO claimant, got %q — the identity was dropped (entity-wide scan)", name)
	}
	name, _, ok := resolveClaimant(project, "omarchy-vm", "check-omarchy-hybrid-gpu-vm", noOverlay)
	if !ok || name != "check-omarchy-hybrid-gpu-vm" {
		t.Fatalf("the hybrid bed must resolve its OWN claim, got name=%q ok=%v", name, ok)
	}
}

// stop/destroy pass the same --domain as create; the resolved claimant is the deploy's OWN
// node, so the release brackets the lease it acquired (never a sibling's).
func TestResolveClaimant_StopDestroySameIdentity(t *testing.T) {
	project := map[string]DeployNode{
		"check-omarchy-hybrid-gpu-vm": claimantNode("omarchy-vm", "nvidia-gpu"),
		"check-omarchy-iso-vm":        claimantNode("omarchy-vm"),
	}
	// The identity a stop/destroy carries is the deploy's domain (bare bed name here).
	createClaim, _, createOK := resolveClaimant(project, "omarchy-vm", "check-omarchy-hybrid-gpu-vm", noOverlay)
	stopClaim, _, stopOK := resolveClaimant(project, "omarchy-vm", "check-omarchy-hybrid-gpu-vm", noOverlay)
	if !createOK || !stopOK || createClaim != stopClaim {
		t.Fatalf("create/stop must resolve the SAME claimant; got create=(%q,%v) stop=(%q,%v)", createClaim, createOK, stopClaim, stopOK)
	}
}

// A per-host overlay's exclusive claim on the SAME identity also resolves (the merge leg of
// the seam), so the identity is honoured through overlay merging — not only the project tree.
func TestResolveClaimant_IdentityThroughOverlay(t *testing.T) {
	overlay := &deploykit.DeployConfig{
		Deploy: map[string]spec.DeployNode{
			"check-omarchy-hybrid-gpu-vm": claimantNode("omarchy-vm", "nvidia-gpu"),
		},
	}
	read := func() (*deploykit.DeployConfig, error) { return overlay, nil }
	name, _, ok := resolveClaimant(nil, "omarchy-vm", "check-omarchy-hybrid-gpu-vm", read)
	if !ok || name != "check-omarchy-hybrid-gpu-vm" {
		t.Fatalf("the overlay's identity-scoped claim must resolve, got name=%q ok=%v", name, ok)
	}
	if _, _, ok := resolveClaimant(nil, "omarchy-vm", "check-omarchy-iso-vm", read); ok {
		t.Fatalf("a different identity must NOT inherit the overlay claim")
	}
}
