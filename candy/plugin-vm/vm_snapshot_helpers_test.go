package vm

import "testing"

// TestIsSnapshotDisk: the snapshot-anchored active-disk detection — the SAME
// file is the keeper's active disk (needs writable) AND the clones' backing
// (needs 0444 for shared locks). The start/create paths chmod it writable
// before qemu opens it and back to 0444 after.
func TestIsSnapshotDisk(t *testing.T) {
	snapshot := []string{
		"/home/u/.local/share/charly/vm/charly-check-omarchy-eval-base-inst/snapshots/golden/disk.qcow2",
		"/home/u/.local/share/charly/vm/charly-x/snapshots/2026.253.1/disk.qcow2",
	}
	for _, p := range snapshot {
		if !isSnapshotDisk(p) {
			t.Fatalf("isSnapshotDisk(%q) = false, want true", p)
		}
	}
	notSnapshot := []string{
		"/home/u/.local/share/charly/vm/charly-x/disk.qcow2",
		"/home/u/.local/share/charly/vm/charly-x/disk2.qcow2",
		"/home/u/.local/share/charly/vm/charly-x/seed.iso",
		"",
	}
	for _, p := range notSnapshot {
		if isSnapshotDisk(p) {
			t.Fatalf("isSnapshotDisk(%q) = true, want false", p)
		}
	}
}

// TestFirstDiskSourceFile: the active-disk path extraction from the domain XML.
func TestFirstDiskSourceFile(t *testing.T) {
	xml := `<domain><devices>
  <disk type='file' device='disk'>
    <driver name='qemu' type='qcow2'/>
    <source file='/vms/x/snapshots/golden/disk.qcow2'/>
    <target dev='vda' bus='virtio'/>
  </disk>
  <disk type='file' device='cdrom'>
    <source file='/vms/x/seed.iso'/>
  </disk>
</devices></domain>`
	got, err := firstDiskSourceFile(xml)
	if err != nil {
		t.Fatalf("firstDiskSourceFile: %v", err)
	}
	if got != "/vms/x/snapshots/golden/disk.qcow2" {
		t.Fatalf("firstDiskSourceFile = %q, want the disk source", got)
	}
}
