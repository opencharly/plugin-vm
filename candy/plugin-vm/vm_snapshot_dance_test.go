package vm

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWithSnapshotWritable: the keeper-restart chmod dance — the snapshot is
// made writable (0644) BEFORE the start fn runs (qemu opens the active disk
// read-write) and restored to 0444 (the shared-clone default) AFTER. This
// test FAILS without the fix: the pre-fix start never chmodded the active
// disk, so a 0444 snapshot could not be opened by qemu (EACCES).
func TestWithSnapshotWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshots", "golden", "disk.qcow2")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("QFI"), 0o444); err != nil {
		t.Fatal(err)
	}
	// The dance: writable during fn, restored to 0444 after.
	var modeDuringFn os.FileMode
	err := withSnapshotWritable(path, func() error {
		st, serr := os.Stat(path)
		if serr != nil {
			return serr
		}
		modeDuringFn = st.Mode().Perm()
		return nil
	})
	if err != nil {
		t.Fatalf("withSnapshotWritable: %v", err)
	}
	if modeDuringFn != 0o644 {
		t.Fatalf("mode during fn = %o, want 0644 (qemu opens the active disk read-write)", modeDuringFn)
	}
	st, serr := os.Stat(path)
	if serr != nil {
		t.Fatal(serr)
	}
	if got := st.Mode().Perm(); got != 0o444 {
		t.Fatalf("mode after fn = %o, want 0444 (the shared-clone default restored)", got)
	}
	// A failing fn still restores the mode (the restore is unconditional).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = withSnapshotWritable(path, func() error { return os.ErrPermission })
	st, _ = os.Stat(path)
	if got := st.Mode().Perm(); got != 0o444 {
		t.Fatalf("mode after a failing fn = %o, want 0444 (restore is unconditional)", got)
	}
}
