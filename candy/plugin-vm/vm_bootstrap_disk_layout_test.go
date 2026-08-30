package vm

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// These call DiskLayoutFromDistro — the SAME function buildBootstrapDisk calls — rather
// than a copy of its logic. An earlier revision mirrored the mapping in a test helper,
// which meant the tests would still have passed if production had diverged from them.
//
// A distro with no disk_layout must produce the historical script. This is the guarantee
// that adding the field changed nothing for arch, cachyos, debian, ubuntu and fedora.
func TestBootstrapDiskLayout_NoDistroBlockIsUnchanged(t *testing.T) {
	for _, d := range []*DistroDef{nil, {}} {
		prelude, _, err := EmitDiskBuildScript(DiskLayoutFromDistro(d, "20G", "btrfs", "/mnt"))
		if err != nil {
			t.Fatalf("EmitDiskBuildScript: %v", err)
		}
		if !strings.Contains(prelude, `mount "${LOOP}p1" /mnt/boot/efi`) {
			t.Errorf("a distro without disk_layout must keep the ESP at /boot/efi:\n%s", prelude)
		}
		if strings.Contains(prelude, "btrfs subvolume create") {
			t.Errorf("a distro without disk_layout must not emit subvolumes:\n%s", prelude)
		}
	}
}

// The Omarchy distro entity's block must reach the emitted script intact.
func TestBootstrapDiskLayout_OmarchyDistroReachesTheScript(t *testing.T) {
	d := &DistroDef{DiskLayout: &spec.DiskLayout{
		EspMountPoint: "/boot",
		Subvolume: []spec.Subvolume{
			{Name: "@", MountPoint: "/", MountOptions: "compress=zstd,noatime"},
			{Name: "@home", MountPoint: "/home", MountOptions: "compress=zstd,noatime"},
			{Name: "@log", MountPoint: "/var/log", MountOptions: "compress=zstd,noatime"},
			{Name: "@pkg", MountPoint: "/var/cache/pacman/pkg", MountOptions: "compress=zstd,noatime"},
		},
	}}
	prelude, finalize, err := EmitDiskBuildScript(DiskLayoutFromDistro(d, "40G", "btrfs", "/mnt"))
	if err != nil {
		t.Fatalf("EmitDiskBuildScript: %v", err)
	}
	for _, want := range []string{
		"btrfs subvolume create /mnt/@\n",
		"btrfs subvolume create /mnt/@pkg\n",
		`mount -o subvol=@,compress=zstd,noatime "${LOOP}p2" /mnt`,
		`mount -o subvol=@pkg,compress=zstd,noatime "${LOOP}p2" /mnt/var/cache/pacman/pkg`,
		`mount "${LOOP}p1" /mnt/boot` + "\n",
	} {
		if !strings.Contains(prelude, want) {
			t.Errorf("the distro's disk_layout did not reach the script — missing %q:\n%s", want, prelude)
		}
	}
	if strings.Contains(prelude, "/mnt/boot/efi") {
		t.Errorf("esp_mount_point /boot was ignored, the script still uses /boot/efi:\n%s", prelude)
	}
	if !strings.Contains(finalize, "umount /mnt/boot\n") {
		t.Errorf("finalize did not unmount the relocated ESP:\n%s", finalize)
	}
}

// A distro declaring subvolumes on a non-btrfs rootfs must fail loudly at build time
// rather than silently produce a filesystem with none.
func TestBootstrapDiskLayout_SubvolumesOnExt4Rejected(t *testing.T) {
	d := &DistroDef{DiskLayout: &spec.DiskLayout{
		Subvolume: []spec.Subvolume{{Name: "@", MountPoint: "/"}},
	}}
	if _, _, err := EmitDiskBuildScript(DiskLayoutFromDistro(d, "10G", "ext4", "/mnt")); err == nil {
		t.Fatal("declaring subvolumes with rootfs ext4 must be rejected")
	}
}
