package vm

import (
	"strings"
	"testing"
)

// The exact script EmitDiskBuildScript produced for a plain btrfs layout BEFORE
// subvolume support existed, captured verbatim. TestEmitDiskBuildScript_NilSubvolumesIsByteIdentical
// asserts the new code still emits this, character for character.
//
// This is the gate for the whole change: subvolume support is only safe to add if every
// existing bootstrap VM — arch, cachyos, debian, ubuntu, fedora — keeps building the same
// disk it built yesterday. A behavioural diff here is a regression in someone else's distro,
// not a feature.
const goldenBtrfsPrelude = `set -euo pipefail
mkdir -p /out
RAW=/out/disk.raw
truncate -s 20G "$RAW"
LOOP=$(losetup --find --show --partscan "$RAW")
trap '
  umount /mnt/boot/efi 2>/dev/null || true
  umount /mnt 2>/dev/null || true
  losetup -d "$LOOP" 2>/dev/null || true
' EXIT
parted -s "$LOOP" \
  mklabel gpt \
  mkpart ESP fat32 1MiB 512MiB \
  set 1 esp on \
  mkpart root btrfs 512MiB 100%
partprobe "$LOOP" || true
mkfs.fat -F32 "${LOOP}p1"
mkfs.btrfs -f "${LOOP}p2"
mkdir -p /mnt
mount "${LOOP}p2" /mnt
mkdir -p /mnt/boot/efi
mount "${LOOP}p1" /mnt/boot/efi
# >>> install rootfs <<<
`

const goldenBtrfsFinalize = `# <<< install rootfs >>>
sync
umount /mnt/boot/efi
umount /mnt
losetup -d "$LOOP"
trap - EXIT
qemu-img convert -O qcow2 "$RAW" /out/disk.qcow2
rm -f "$RAW"
`

func TestEmitDiskBuildScript_NilSubvolumesIsByteIdentical(t *testing.T) {
	prelude, finalize, err := EmitDiskBuildScript(DiskLayout{
		SizeBytesOrSuffix: "20G",
		Rootfs:            "btrfs",
	})
	if err != nil {
		t.Fatalf("EmitDiskBuildScript: %v", err)
	}
	if prelude != goldenBtrfsPrelude {
		t.Errorf("prelude changed for a nil-Subvolumes layout.\n--- got ---\n%s\n--- want ---\n%s", prelude, goldenBtrfsPrelude)
	}
	if finalize != goldenBtrfsFinalize {
		t.Errorf("finalize changed for a nil-Subvolumes layout.\n--- got ---\n%s\n--- want ---\n%s", finalize, goldenBtrfsFinalize)
	}
}

// ext4 is the default rootfs for most distros and must be equally untouched.
func TestEmitDiskBuildScript_Ext4Unaffected(t *testing.T) {
	prelude, _, err := EmitDiskBuildScript(DiskLayout{SizeBytesOrSuffix: "10G"})
	if err != nil {
		t.Fatalf("EmitDiskBuildScript: %v", err)
	}
	for _, want := range []string{
		"mkfs.ext4 -F \"${LOOP}p2\"",
		"mount \"${LOOP}p2\" /mnt",
		"mount \"${LOOP}p1\" /mnt/boot/efi",
	} {
		if !strings.Contains(prelude, want) {
			t.Errorf("ext4 prelude lost %q:\n%s", want, prelude)
		}
	}
	if strings.Contains(prelude, "btrfs subvolume create") {
		t.Errorf("ext4 prelude must not emit subvolume commands:\n%s", prelude)
	}
}

// The Omarchy layout, which is why this change exists: ESP at /boot (not /boot/efi) and
// the @/@home/@log/@pkg subvolumes an ISO-installed Omarchy guest actually has.
func TestEmitDiskBuildScript_OmarchyLayout(t *testing.T) {
	prelude, finalize, err := EmitDiskBuildScript(DiskLayout{
		SizeBytesOrSuffix: "40G",
		Rootfs:            "btrfs",
		EspMountPoint:     "/boot",
		Subvolumes: []Subvolume{
			{Name: "@", MountPoint: "/", MountOptions: "compress=zstd,noatime"},
			{Name: "@home", MountPoint: "/home", MountOptions: "compress=zstd,noatime"},
			{Name: "@log", MountPoint: "/var/log", MountOptions: "compress=zstd,noatime"},
			{Name: "@pkg", MountPoint: "/var/cache/pacman/pkg", MountOptions: "compress=zstd,noatime"},
		},
	})
	if err != nil {
		t.Fatalf("EmitDiskBuildScript: %v", err)
	}

	// The ordering IS the correctness. Creating a subvolume through a mount already
	// pinned to a different subvolume fails, so the top level must be mounted, the
	// subvolumes created, that mount dropped, and only then the root subvolume mounted.
	for _, step := range []string{
		"mount \"${LOOP}p2\" /mnt\n",
		"btrfs subvolume create /mnt/@\n",
		"btrfs subvolume create /mnt/@home\n",
		"btrfs subvolume create /mnt/@log\n",
		"btrfs subvolume create /mnt/@pkg\n",
		"umount /mnt\n",
		"mount -o subvol=@,compress=zstd,noatime \"${LOOP}p2\" /mnt\n",
		"mount -o subvol=@home,compress=zstd,noatime \"${LOOP}p2\" /mnt/home\n",
		"mount -o subvol=@pkg,compress=zstd,noatime \"${LOOP}p2\" /mnt/var/cache/pacman/pkg\n",
	} {
		if !strings.Contains(prelude, step) {
			t.Errorf("prelude missing %q:\n%s", step, prelude)
		}
	}
	if i, j := strings.Index(prelude, "btrfs subvolume create /mnt/@\n"), strings.Index(prelude, "umount /mnt\n"); i < 0 || j < 0 || i > j {
		t.Errorf("subvolume creation must precede the top-level umount:\n%s", prelude)
	}
	if i, j := strings.Index(prelude, "umount /mnt\n"), strings.Index(prelude, "mount -o subvol=@,"); i < 0 || j < 0 || i > j {
		t.Errorf("the root subvolume must be mounted AFTER the top-level umount:\n%s", prelude)
	}

	// The ESP goes where the distro says, not where the old hardcode said.
	if !strings.Contains(prelude, "mount \"${LOOP}p1\" /mnt/boot\n") {
		t.Errorf("ESP not mounted at the requested /boot:\n%s", prelude)
	}
	if strings.Contains(prelude, "/mnt/boot/efi") {
		t.Errorf("ESP must NOT fall back to /boot/efi when EspMountPoint is set:\n%s", prelude)
	}

	// Unmounting has to run deepest-first or the root subvolume is busy and the qcow2
	// conversion runs against a filesystem with dirty mounts still attached.
	iEsp := strings.Index(finalize, "umount /mnt/boot\n")
	iHome := strings.Index(finalize, "umount /mnt/home\n")
	iRoot := strings.Index(finalize, "umount /mnt\n")
	if iEsp < 0 || iHome < 0 || iRoot < 0 {
		t.Fatalf("finalize missing an unmount:\n%s", finalize)
	}
	if iEsp >= iHome || iHome >= iRoot {
		t.Errorf("finalize must unmount ESP, then nested subvolumes, then root:\n%s", finalize)
	}
}

// Teeth: the invariants that make a silently-unbootable disk impossible to declare.
func TestEmitDiskBuildScript_SubvolumeValidation(t *testing.T) {
	cases := []struct {
		name   string
		layout DiskLayout
		want   string
	}{
		{
			name: "subvolumes on ext4 are rejected rather than ignored",
			layout: DiskLayout{SizeBytesOrSuffix: "10G", Rootfs: "ext4",
				Subvolumes: []Subvolume{{Name: "@", MountPoint: "/"}}},
			want: "require rootfs btrfs",
		},
		{
			name: "no root subvolume leaves nothing to mount the others into",
			layout: DiskLayout{SizeBytesOrSuffix: "10G", Rootfs: "btrfs",
				Subvolumes: []Subvolume{{Name: "@home", MountPoint: "/home"}}},
			want: "exactly one subvolume must mount",
		},
		{
			name: "two root subvolumes are ambiguous",
			layout: DiskLayout{SizeBytesOrSuffix: "10G", Rootfs: "btrfs",
				Subvolumes: []Subvolume{{Name: "@", MountPoint: "/"}, {Name: "@alt", MountPoint: "/"}}},
			want: "exactly one subvolume must mount",
		},
		{
			name: "a relative mount point would mount outside the tree",
			layout: DiskLayout{SizeBytesOrSuffix: "10G", Rootfs: "btrfs",
				Subvolumes: []Subvolume{{Name: "@", MountPoint: "/"}, {Name: "@home", MountPoint: "home"}}},
			want: "is not absolute",
		},
		{
			name: "an unnamed subvolume cannot be created",
			layout: DiskLayout{SizeBytesOrSuffix: "10G", Rootfs: "btrfs",
				Subvolumes: []Subvolume{{Name: "@", MountPoint: "/"}, {Name: "", MountPoint: "/home"}}},
			want: "empty name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := EmitDiskBuildScript(tc.layout)
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
