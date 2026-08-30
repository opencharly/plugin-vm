package vm

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/opencharly/spec/spec"
)

// harnessPreamble makes a udev-less container able to run the emitted script UNMODIFIED.
//
// Two gaps, both properties of the container and neither of the script:
//   - /dev carries only loop0, and `losetup --find` hands out the next free minor whether
//     or not a node exists for it.
//   - loop PARTITION nodes are created by udev, which a container does not run, so
//     `mkfs.fat "${LOOP}p1"` fails with "No such file or directory".
//
// kpartx creates device-mapper nodes for the partitions without udev; symlinking them to
// the names the script uses lets the emitted script run byte-for-byte. The shim wraps
// partprobe because that is the point in the script where a partition table exists but
// has not yet been used. Nothing about the script under test is rewritten.
const harnessPreamble = `set -euo pipefail
mkdir -p /out
for n in 0 1 2 3 4 5 6 7; do [ -e /dev/loop$n ] || mknod /dev/loop$n b 7 "$n"; done
partprobe() {
  command partprobe "$@" 2>/dev/null || true
  for dev in "$@"; do
    kpartx -as "$dev" >/dev/null 2>&1 || true
    base=$(basename "$dev")
    for p in 1 2; do
      [ -e "/dev/mapper/${base}p${p}" ] && ln -sf "/dev/mapper/${base}p${p}" "/dev/${base}p${p}"
    done
  done
  return 0
}
`

// assertions run between the emitted prelude and its finalize, in place of the rootfs
// install the real build does there.
const assertions = `
echo "--- mounts the emitted script produced ---"
for m in /mnt /mnt/home /mnt/var/log /mnt/var/cache/pacman/pkg; do
  printf '%-28s %s\n' "$m" "$(findmnt -no OPTIONS "$m")"
done
printf '%-28s %s\n' /mnt/boot "$(findmnt -no FSTYPE /mnt/boot)"
findmnt -no FSTYPE /mnt/boot | grep -qx vfat && echo ESP-IS-VFAT
test ! -e /mnt/boot/efi && echo NO-BOOT-EFI
btrfs subvolume list /mnt
`

// TestDiskLayoutFromDistro_LiveOmarchy runs the PRODUCTION mapping, and the script it
// produces, against real block devices.
//
// Guarded by CHARLY_LIVE_DISK=1: it needs a privileged container and loop devices, which
// CI has no business allocating. A test that silently no-ops would be worse than one that
// says why it skipped.
//
// What this proves that the unit tests cannot: the layout charly's omarchy distro entity
// declares, fed through DiskLayoutFromDistro — the same call buildBootstrapDisk makes —
// yields a script the kernel actually accepts.
func TestDiskLayoutFromDistro_LiveOmarchy(t *testing.T) {
	if os.Getenv("CHARLY_LIVE_DISK") != "1" {
		t.Skip("set CHARLY_LIVE_DISK=1 to run: needs a privileged container and loop devices")
	}

	// The omarchy entity's disk_layout exactly as authored in charly/charly.yml, parsed
	// from YAML rather than hand-built so this consumes the same wire shape the loader
	// does. A field renamed in the schema breaks this test, which is the point.
	const authored = `
esp_mount_point: /boot
subvolume:
    - {name: "@", mount_point: /, mount_options: "compress=zstd,noatime"}
    - {name: "@home", mount_point: /home, mount_options: "compress=zstd,noatime"}
    - {name: "@log", mount_point: /var/log, mount_options: "compress=zstd,noatime"}
    - {name: "@pkg", mount_point: /var/cache/pacman/pkg, mount_options: "compress=zstd,noatime"}
`
	var dl spec.DiskLayout
	if err := yaml.Unmarshal([]byte(authored), &dl); err != nil {
		t.Fatalf("parsing the authored disk_layout: %v", err)
	}
	if dl.EspMountPoint != "/boot" || len(dl.Subvolume) != 4 {
		t.Fatalf("the authored block did not round-trip into spec.DiskLayout: %+v", dl)
	}

	prelude, finalize, err := EmitDiskBuildScript(
		DiskLayoutFromDistro(&DistroDef{DiskLayout: &dl}, "2G", "btrfs", "/mnt"))
	if err != nil {
		t.Fatalf("EmitDiskBuildScript: %v", err)
	}

	cmd := exec.Command("sudo", "-n", "podman", "run", "--rm", "-i", "--privileged", "--network", "host",
		"quay.io/archlinux/archlinux:latest", "sh", "-c",
		"pacman -Sy --noconfirm --needed parted btrfs-progs dosfstools util-linux multipath-tools qemu-img >/dev/null 2>&1; bash -s")
	cmd.Stdin = strings.NewReader(harnessPreamble + prelude + assertions + finalize + "\necho TEARDOWN-CLEAN\n")
	out, err := cmd.CombinedOutput()
	t.Logf("live run:\n%s", out)
	if err != nil {
		t.Fatalf("the emitted script failed: %v", err)
	}
	for _, want := range []string{
		"subvol=/@", "subvol=/@home", "subvol=/@log", "subvol=/@pkg",
		"ESP-IS-VFAT", "NO-BOOT-EFI", "TEARDOWN-CLEAN",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("live output missing %q", want)
		}
	}
}
