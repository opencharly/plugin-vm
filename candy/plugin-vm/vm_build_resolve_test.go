package vm

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// TestVmBuildDeployFromHop gates the vm-build deploy-from hop SEAM (Phase 3): a from:
// name:tag DRIVE target names the clone-base BED (a deploy whose own from: names the
// terminal kind:vm template) — the ONE chain resolver (loaderkit.DeployTargetEntity, the
// same seam the vm-build step threads) must resolve the terminal template. The seam is
// the testable unit; the wiring (resolveVmBuildViaDeployFrom calling it) is thin and
// covered by the live vm-build path.
func TestVmBuildDeployFromHop(t *testing.T) {
	uf := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"check-vm-clone-base": {From: "cachyos-vm"},
		},
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"cachyos-vm": json.RawMessage("{}")},
		},
	}
	target, ok := loaderkit.DeployTargetEntity(uf, "check-vm-clone-base")
	if !ok || target != "cachyos-vm" {
		t.Fatalf("DeployTargetEntity(check-vm-clone-base) = (%q, %v), want (cachyos-vm, true) — the terminal template after the hop", target, ok)
	}
}

// TestVmBuildDeployFromHop_Qualified gates the NAMESPACE-QUALIFIED hop (the
// git-linked import form): a bed cloning from omarchy.check-charly-omarchy-vm
// (a kind:check bed in the imported namespace) must resolve to the qualified
// terminal template omarchy.omarchy-vm. This test FAILS without the
// namespace-aware DeployTargetEntity (sdk #247) — the runtime counterpart of
// the load-time ResolveEntityRef.
func TestVmBuildDeployFromHop_Qualified(t *testing.T) {
	ns := &spec.UnifiedFile{
		Deploy: map[string]spec.DeployNode{
			"check-charly-omarchy-vm": {From: "omarchy-vm"},
		},
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"omarchy-vm": json.RawMessage("{}")},
		},
	}
	uf := &spec.UnifiedFile{
		Namespaces: map[string]*spec.UnifiedFile{"omarchy": ns},
	}
	target, ok := loaderkit.DeployTargetEntity(uf, "omarchy.check-charly-omarchy-vm")
	if !ok || target != "omarchy.omarchy-vm" {
		t.Fatalf("DeployTargetEntity(omarchy.check-charly-omarchy-vm) = (%q, %v), want (omarchy.omarchy-vm, true) — the qualified terminal template after the hop", target, ok)
	}
	// The unqualified form stays local-only (the no-leak contract).
	if _, ok := loaderkit.DeployTargetEntity(uf, "check-charly-omarchy-vm"); ok {
		t.Fatal("unqualified namespace bed leaked into the local scope")
	}
}

// TestResolveKindEntityBody_Qualified gates the CHANGED LINE directly:
// resolveVmBuildEntity's body lookup is loaderkit.ResolveKindEntityBody(uf,
// "vm", boxName) — a namespace-qualified box name (ns.entity) must resolve the
// namespace's own template body. This test FAILS without the namespace-aware
// ResolveKindEntityBody (sdk #247).
func TestResolveKindEntityBody_Qualified(t *testing.T) {
	ns := &spec.UnifiedFile{
		PluginKinds: map[string]map[string]json.RawMessage{
			"vm": {"omarchy-vm": json.RawMessage("{}")},
		},
	}
	uf := &spec.UnifiedFile{
		Namespaces: map[string]*spec.UnifiedFile{"omarchy": ns},
	}
	body, ok := loaderkit.ResolveKindEntityBody(uf, "vm", "omarchy.omarchy-vm")
	if !ok || len(body) == 0 {
		t.Fatal("ResolveKindEntityBody(omarchy.omarchy-vm) did not resolve the namespace template body")
	}
	// The unqualified form stays local-only (the no-leak contract).
	if _, ok := loaderkit.ResolveKindEntityBody(uf, "vm", "omarchy-vm"); ok {
		t.Fatal("unqualified namespace body leaked into the local scope")
	}
}

// TestEntityLeaf gates the leaf-stripping for the clone drive: a
// namespace-qualified ref (ns.entity) keys the snapshot registry + disk paths
// by the LEAF (the name as authored in the owning repo); an unqualified name
// is its own leaf.
func TestEntityLeaf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"omarchy.check-charly-omarchy-vm", "check-charly-omarchy-vm"},
		{"omarchy.omarchy-vm", "omarchy-vm"},
		{"check-omarchy-eval-base-inst", "check-omarchy-eval-base-inst"},
		{"", ""},
	}
	for _, c := range cases {
		if got := entityLeaf(c.in); got != c.want {
			t.Fatalf("entityLeaf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestApplyCloneDriveSource_Qualified gates the clone drive's leaf wiring: the
// drive's FromVm is the LEAF of a namespace-qualified ref (the snapshot
// registry key), not the qualified ref itself.
func TestApplyCloneDriveSource_Qualified(t *testing.T) {
	vs := &VmSpec{}
	applyCloneDriveSource(vs, "omarchy.check-charly-omarchy-vm", "golden")
	if vs.Source.Kind != "clone" || vs.Source.FromVm != "check-charly-omarchy-vm" || vs.Source.FromSnapshot != "golden" {
		t.Errorf("drive source = kind=%q from_vm=%q from_snapshot=%q, want clone/check-charly-omarchy-vm/golden",
			vs.Source.Kind, vs.Source.FromVm, vs.Source.FromSnapshot)
	}
}

// TestBaseDiskPath gates the vm-create base-disk path: a namespace-qualified
// ref keys the entity dir by the LEAF (the vm-build clone drive writes the
// disk there). This test FAILS without the leaf-stripping in baseDiskPath.
func TestBaseDiskPath(t *testing.T) {
	got := baseDiskPath("omarchy.check-charly-omarchy-vm")
	want := filepath.Join(vmDiskDir("check-charly-omarchy-vm"), "disk.qcow2")
	if got != want {
		t.Fatalf("baseDiskPath(qualified) = %q, want %q (the leaf-keyed path)", got, want)
	}
	if got := baseDiskPath("check-omarchy-eval-base-inst"); got != filepath.Join(vmDiskDir("check-omarchy-eval-base-inst"), "disk.qcow2") {
		t.Fatalf("baseDiskPath(local) = %q, want the unchanged local path", got)
	}
}
