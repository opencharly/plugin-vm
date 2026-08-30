package vm

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// buildDiskLayoutFromDistro mirrors the mapping in buildBootstrapDisk. It is factored into
// the test rather than exported because the production site is three lines inside a
// function that also runs a privileged container; what needs pinning is the MAPPING, and
// the emitter it feeds is already covered by vm_disk_subvolume_test.go.
//
// If buildBootstrapDisk's mapping ever diverges from this, the assertions below stop
// describing production — so the test asserts against EmitDiskBuildScript's real output,
// which is the same call the production path makes.
func layoutFromDistro(d *DistroDef, size, rootfs string) DiskLayout {
	layout := DiskLayout{SizeBytesOrSuffix: size, Rootfs: rootfs, Mnt: "/mnt"}
	if d != nil && d.DiskLayout != nil {
		layout.EspMountPoint = d.DiskLayout.EspMountPoint
		for _, sv := range d.DiskLayout.Subvolume {
			layout.Subvolumes = append(layout.Subvolumes, Subvolume{
				Name: sv.Name, MountPoint: sv.MountPoint, MountOptions: sv.MountOptions,
			})
		}
	}
	return layout
}

// A distro with no disk_layout must produce the historical script. This is the guarantee
// that adding the field changed nothing for arch, cachyos, debian, ubuntu and fedora.
func TestBootstrapDiskLayout_NoDistroBlockIsUnchanged(t *testing.T) {
	for _, d := range []*DistroDef{nil, {}} {
		prelude, _, err := EmitDiskBuildScript(layoutFromDistro(d, "20G", "btrfs"))
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
	prelude, finalize, err := EmitDiskBuildScript(layoutFromDistro(d, "40G", "btrfs"))
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
	if _, _, err := EmitDiskBuildScript(layoutFromDistro(d, "10G", "ext4")); err == nil {
		t.Fatal("declaring subvolumes with rootfs ext4 must be rejected")
	}
}
