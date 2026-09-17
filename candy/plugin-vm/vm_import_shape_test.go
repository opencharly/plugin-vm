package vm

import (
	"os"
	"path/filepath"
	"strings"
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

// TestMergeImportedVmIntoDoc_NameFirstUpdate is the R7 coverage for the UPDATE path's
// name-first rewrite: mergeImportedVmIntoDoc must locate `<name>.vm` (NOT a top-level `vm:`
// map) and write the cpu count under `cpu:` (NOT `cpus:`). A pre-fix shape (top-level map /
// `cpus:`) fails this.
func TestMergeImportedVmIntoDoc_NameFirstUpdate(t *testing.T) {
	raw := `version: 2026.249.2125
my-vm:
    vm:
        source:
            kind: imported
            libvirt_name: old
            disk_path: /old.qcow2
            disk_format: qcow2
        ram: 1G
        cpu: 1
        ssh: {user: user}
`
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	root := &yaml.Node{}
	if err := yaml.Unmarshal([]byte(raw), root); err != nil {
		t.Fatal(err)
	}
	topMap := root.Content[0]

	fresh := &VmSpec{Ram: "8G", Cpus: 4, Machine: "q35", Firmware: "uefi-insecure"}
	fresh.Source.Kind = "imported"
	fresh.Source.LibvirtName = "new-dom"
	fresh.Source.DiskPath = "/new.qcow2"
	fresh.Source.DiskFormat = "qcow2"
	fresh.Source.AdoptedAt = "" // should be preserved from the existing entry
	if err := mergeImportedVmIntoDoc(topMap, "my-vm", "my-vm", fresh, false); err != nil {
		t.Fatalf("mergeImportedVmIntoDoc: %v", err)
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]yaml.Node
	if err := yaml.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, bad := got["vm"]; bad {
		t.Fatalf("update wrote a legacy top-level `vm:` map:\n%s", out)
	}
	entry := got["my-vm"]
	var body map[string]yaml.Node
	if err := entry.Decode(&body); err != nil {
		t.Fatal(err)
	}
	var vmBody map[string]yaml.Node
	vmn := body["vm"]
	if err := vmn.Decode(&vmBody); err != nil {
		t.Fatal(err)
	}
	if _, bad := vmBody["cpus"]; bad {
		t.Errorf("update wrote `cpus:` — the schema field is `cpu:`:\n%s", out)
	}
	if got := vmBody["cpu"].Value; got != "4" {
		t.Errorf("cpu = %q, want 4:\n%s", got, out)
	}
	if got := vmBody["ram"].Value; got != "8G" {
		t.Errorf("ram = %q, want 8G", got)
	}
	// operator-authored sibling preserved
	if _, ok := vmBody["ssh"]; !ok {
		t.Errorf("operator-authored `ssh:` sibling was dropped:\n%s", out)
	}
}

// TestMergeImportedVmIntoDoc_MissingEntryNamesIt proves the update path names a missing entry
// (not a silent no-op).
func TestMergeImportedVmIntoDoc_MissingEntryNamesIt(t *testing.T) {
	root := &yaml.Node{}
	if err := yaml.Unmarshal([]byte("version: 2026.249.2125\n"), root); err != nil {
		t.Fatal(err)
	}
	err := mergeImportedVmIntoDoc(root.Content[0], "absent", "absent", &VmSpec{}, false)
	if err == nil {
		t.Fatal("a missing entry must error")
	}
	if !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("error = %v, want it to name the missing entry", err)
	}
}

// TestMapOSToSpec_NormalizesMachine exercises the CALL SITE (mapOSToSpec), so reverting the
// normalization there fails this test — `charly vm import` copies libvirt state verbatim, and
// libvirt reports a VERSIONED machine string ("pc-q35-11.1") the schema's closed enum rejects,
// so the emitted config would fail to load.
func TestMapOSToSpec_NormalizesMachine(t *testing.T) {
	cases := map[string]string{
		"pc-q35-11.1":   "q35",
		"pc-q35-8.2":    "q35",
		"q35":           "q35",
		"pc-i440fx-8.2": "i440fx",
		"i440fx":        "i440fx",
		"pc":            "pc",
		"virt":          "virt",
		"weird-unknown": "", // dropped, never emitted as a schema-invalid value
	}
	for in, want := range cases {
		var s VmSpec
		mapOSToSpec(libvirtOSForImport{Type: libvirtOSTypeForImport{Machine: in}}, &s)
		if s.Machine != want {
			t.Errorf("mapOSToSpec(machine=%q) -> Machine %q, want %q", in, s.Machine, want)
		}
	}
}
