package vm

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/opencharly/spec/spec"
)

// TestVmBackendRegistryDrift pins the provider registry against the SCHEMA
// vocabulary. Mutation: adding a provider key not in spec.VmBackends, or
// removing one, fails — so the class cannot drift from #VmBackendName.
func TestVmBackendRegistryDrift(t *testing.T) {
	got := make([]string, 0, len(vmBackendProviders))
	for word := range vmBackendProviders {
		got = append(got, string(word))
	}
	slices.Sort(got)
	want := slices.Clone(spec.VmBackends)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("provider registry = %v, schema VmBackends = %v", got, want)
	}
	for word, provider := range vmBackendProviders {
		if provider.Word() != word {
			t.Errorf("provider registered under %q reports Word()=%q", word, provider.Word())
		}
	}
}

// TestVmBackendAutoExcludesKrun pins krun as explicit-only. Mutation: marking
// krun auto-detectable fails, because `auto` would then probe it first when
// libvirt/qemu are absent.
func TestVmBackendAutoExcludesKrun(t *testing.T) {
	krun := vmBackendProviders[vmBackendKrun]
	if krun == nil {
		t.Fatal("krun provider is not registered")
	}
	if krun.AutoDetectable() {
		t.Fatal("krun must never be auto-detected")
	}
	for _, word := range []spec.VmBackend{vmBackendLibvirt, vmBackendQemu} {
		if p := vmBackendProviders[word]; p == nil || !p.AutoDetectable() {
			t.Fatalf("%q must be auto-detectable", word)
		}
	}
}

// TestVmBackendProviderForRejectsUnknown pins the hard error that replaced the
// old silent fall-through (the krun defect: every switch had no arm).
func TestVmBackendProviderForRejectsUnknown(t *testing.T) {
	if _, err := vmBackendProviderFor("bogus"); err == nil {
		t.Fatal("unknown backend must error")
	}
	if _, err := vmBackendProviderFor(""); err == nil {
		t.Fatal("empty backend must error")
	}
	p, err := vmBackendProviderFor(string(vmBackendKrun))
	if err != nil || p.Word() != vmBackendKrun {
		t.Fatalf("explicit krun => (%v, %v)", p, err)
	}
}

// TestVmBackendHolderSeparatesKrunFromQemu is the integration guard: a krun
// state dir (krun-plan.json) must NOT be claimed by the qemu provider, and vice
// versa. Mutation: reverting qemu's "any directory is qemu" probe makes the
// krun arm return qemu.
func TestVmBackendHolderSeparatesKrunFromQemu(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	stubKrunContainerState(t, false)

	krunName := "charly-krun-fixture"
	krunDir := filepath.Join(root, krunName)
	if err := os.MkdirAll(krunDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(krunStatePath(krunDir), []byte(`{"name":"charly-krun-fixture"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, _, _ := (qemuBackend{}).Holder(krunName); found {
		t.Fatal("qemu claimed a krun state dir")
	}
	if found, _, _ := (krunBackend{}).Holder(krunName); !found {
		t.Fatal("krun did not claim its own state dir")
	}

	qemuName := "charly-qemu-fixture"
	qemuDir := filepath.Join(root, qemuName)
	if err := os.MkdirAll(qemuDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qemuDir, "command"), []byte("qemu-system-x86_64"), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, _, _ := (krunBackend{}).Holder(qemuName); found {
		t.Fatal("krun claimed a qemu state dir")
	}
	if found, _, _ := (qemuBackend{}).Holder(qemuName); !found {
		t.Fatal("qemu did not claim its own state dir")
	}
}
