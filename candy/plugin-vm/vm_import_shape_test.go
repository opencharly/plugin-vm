package vm

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestWriteVmImportDeclaration_NameFirstShape is the R7 regression guard for the
// schema-compaction cutover: the writer MUST emit a NAME-FIRST node
// (`<name>: { vm: { … } }`) — the only shape the loader accepts. The predecessor wrote a
// top-level `vm:` map, which the loader HARD-REJECTS with "no kind discriminator", so
// `charly vm import` produced a config that could never load.
func TestWriteVmImportDeclaration_NameFirstShape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "charly.yml"), []byte("version: 2026.249.2125\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	spec := &VmSpec{Ram: "4G", Cpus: 2, Machine: "q35", Firmware: "uefi-insecure"}
	spec.Source.Kind = "imported"
	spec.Source.LibvirtName = "my-domain"
	spec.Source.DiskPath = "/tmp/x.qcow2"
	spec.Source.DiskFormat = "qcow2"
	if err := WriteVmImportDeclaration("my-vm", spec); err != nil {
		t.Fatalf("WriteVmImportDeclaration: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "charly.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written charly.yml does not parse: %v", err)
	}
	// The predecessor's bug: a TOP-LEVEL `vm:` map key.
	if _, bad := doc["vm"]; bad {
		t.Fatalf("writer emitted a legacy top-level `vm:` map — the loader rejects it\n%s", string(raw))
	}
	node, ok := doc["my-vm"]
	if !ok {
		t.Fatalf("no name-first `my-vm` node in the written config\n%s", string(raw))
	}
	n := node // addressable copy (map values are not addressable)
	var body map[string]yaml.Node
	if err := n.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["vm"]; !ok {
		t.Fatalf("`my-vm` node carries no `vm:` kind key\n%s", string(raw))
	}
	// The cpu field must be spelled `cpu` (the schema field), not `cpus`.
	vmn := body["vm"]
	var vmBody map[string]yaml.Node
	if err := vmn.Decode(&vmBody); err != nil {
		t.Fatal(err)
	}
	if _, bad := vmBody["cpus"]; bad {
		t.Errorf("writer emitted `cpus:` — the schema field is `cpu:`\n%s", string(raw))
	}
	if _, ok := vmBody["cpu"]; !ok {
		t.Errorf("writer omitted `cpu:`\n%s", string(raw))
	}
}
