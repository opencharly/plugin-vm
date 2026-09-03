package vm

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// vm_clone_test.go — the clone build path (source.kind == "clone").
//
// BuildClone is the VM-on-VM primitive: it materializes a fresh per-VM qcow2
// with the parent snapshot's external disk as backing chain, bumps the parent
// snapshot's refcount, and regenerates the cloud-init seed ISO with a fresh
// instance-id. These tests exercise the guards that keep a malformed clone
// declaration from failing deep inside qemu-img.

// cloneSpec builds a minimal clone VmSpec. The parent snapshot need not exist
// for the guard tests below — they fail before any registry lookup.
func cloneSpec() *VmSpec {
	s := &VmSpec{}
	s.Source.Kind = "clone"
	s.Source.FromVm = "base-vm"
	s.Source.FromSnapshot = "golden"
	s.DiskSize = "20G"
	return s
}

// The kind guard. Every sibling engine has one, and it is what keeps a dispatch
// bug from silently building the wrong thing.
func TestBuildClone_WrongSourceKindIsRejected(t *testing.T) {
	s := cloneSpec()
	s.Source.Kind = "cloud_image"
	err := BuildClone("clone-vm", s, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("BuildClone must reject a non-clone source.kind")
	}
	if !strings.Contains(err.Error(), "want clone") {
		t.Fatalf("the error must name the expected kind; got: %v", err)
	}
}

// A clone declaration without from_vm is a malformed entity — refuse at the
// top of the build, not after a registry lookup.
func TestBuildClone_RequiresFromVm(t *testing.T) {
	s := cloneSpec()
	s.Source.FromVm = ""
	err := BuildClone("clone-vm", s, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("a clone without source.from_vm must be rejected")
	}
	if !strings.Contains(err.Error(), "from_vm") {
		t.Fatalf("the error must name the missing field; got: %v", err)
	}
}

// A clone declaration without from_snapshot is a malformed entity — the whole
// point of the clone source is that the base is a NAMED snapshot, so an
// unnamed base is an author-time error.
func TestBuildClone_RequiresFromSnapshot(t *testing.T) {
	s := cloneSpec()
	s.Source.FromSnapshot = ""
	err := BuildClone("clone-vm", s, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("a clone without source.from_snapshot must be rejected")
	}
	if !strings.Contains(err.Error(), "from_snapshot") {
		t.Fatalf("the error must name the missing field; got: %v", err)
	}
}

// The build dispatch enumerates its supported kinds in knownVmSourceKinds for
// the unsupported-kind error message. A new source kind that forgets to add
// itself here would be rejected by `charly vm build` with a confusing message
// even though the engine exists — this guard keeps the enumeration honest.
func TestKnownVmSourceKinds_IncludesClone(t *testing.T) {
	if !slices.Contains(knownVmSourceKinds, "clone") {
		t.Fatalf("knownVmSourceKinds must include %q so `charly vm build` dispatches clone entities; got %v", "clone", knownVmSourceKinds)
	}
}

// declaredSnapshotsToCapture is the idempotent-capture decision behind
// `charly vm snapshot capture-declared`: a declared snapshot already present
// in the registry is skipped (the existing baseline is kept), so a re-run is a
// no-op. This is the pure logic the command's registry lookup feeds.
func TestDeclaredSnapshotsToCapture(t *testing.T) {
	declared := []spec.VmSnapshot{
		{Name: "golden"},
		{Name: "baseline"},
		{Name: "fresh"},
	}
	existing := map[string]bool{"golden": true, "baseline": true}
	lookup := func(name string) error {
		if existing[name] {
			return nil
		}
		return fmt.Errorf("not found")
	}

	todo, skipped := declaredSnapshotsToCapture(declared, lookup)
	if len(todo) != 1 || todo[0].Name != "fresh" {
		t.Fatalf("only the not-yet-captured snapshot should be todo; got %+v", todo)
	}
	if len(skipped) != 2 || skipped[0].Name != "golden" || skipped[1].Name != "baseline" {
		t.Fatalf("the already-captured snapshots must be reported as skipped; got %+v", skipped)
	}

	// All captured → nothing to do (idempotent re-run), everything skipped.
	all, allSkipped := declaredSnapshotsToCapture(declared, func(string) error { return nil })
	if len(all) != 0 || len(allSkipped) != len(declared) {
		t.Fatalf("a re-run with every snapshot captured must be a no-op; todo=%+v skipped=%+v", all, allSkipped)
	}

	// None captured → everything is todo, nothing skipped.
	none, noneSkipped := declaredSnapshotsToCapture(declared, func(string) error { return fmt.Errorf("missing") })
	if len(none) != len(declared) || len(noneSkipped) != 0 {
		t.Fatalf("with no snapshots captured, every declared snapshot must be todo; todo=%+v skipped=%+v", none, noneSkipped)
	}
}

