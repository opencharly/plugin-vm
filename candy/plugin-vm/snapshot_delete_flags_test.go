package vm

import (
	"testing"

	"github.com/digitalocean/go-libvirt"
)

// TestSnapshotDeleteFlags_MetadataOnly pins the metadata-only delete flag (B12):
// a FULL delete (flags 0) unlinks the snapshot disk and hangs when it is the running
// domain's in-use backing (measured). This test FAILS if the flag is reverted to 0.
func TestSnapshotDeleteFlags_MetadataOnly(t *testing.T) {
	if snapshotDeleteFlags() != libvirt.DomainSnapshotDeleteMetadataOnly {
		t.Fatalf("snapshot delete must be metadata-only, got %v", snapshotDeleteFlags())
	}
}
