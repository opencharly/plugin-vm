package vm

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/opencharly/sdk/buildkit"
)

// vm_disk_layout.go — the partitioned-disk script emitter (P8b-rest: ported verbatim from
// charly/vm_disk_builder.go; pure text/template rendering, zero core dependency).

// DiskLayout describes a partitioned VM disk to be built inside a privileged container by
// EmitDiskBuildScript. Used by both the pacstrap/debootstrap VM-bootstrap path and the
// bootc-VM install path (where the bootloader is provided by `bootc install` rather than by
// chroot grub-install).
type DiskLayout struct {
	// SizeBytesOrSuffix is the size to allocate for the raw disk file (e.g. "20G", "10240M",
	// "536870912"). Forwarded verbatim to `truncate -s`.
	SizeBytesOrSuffix string
	// Rootfs selects the root partition filesystem: "ext4" (default), "xfs", or "btrfs".
	Rootfs string
	// EspSizeMib sizes the EFI System Partition (FAT32). Default 512.
	EspSizeMib int
	// Mnt is the absolute path inside the container where the root partition gets mounted
	// (default /mnt). Bootloader install templates render against this.
	Mnt string
	// EspMountPoint is the guest-absolute path the ESP is mounted at, relative to the
	// root filesystem (default "/boot/efi"). Distros differ and the difference is not
	// cosmetic: Omarchy installs its ESP at "/boot", which is what limine-entry-tool and
	// ESP_PATH assume, and a loader written to the wrong place leaves an unbootable disk
	// with no build-time error.
	EspMountPoint string
	// Subvolumes, when non-empty AND Rootfs is "btrfs", makes the root filesystem a
	// subvolume layout instead of a bare filesystem: the top level is mounted, every
	// subvolume is created, and the one whose MountPoint is "/" is then remounted at Mnt
	// with its options, with the rest mounted beneath it.
	//
	// Nil reproduces the previous script byte-for-byte, so every existing bootstrap VM is
	// unaffected. Ignored for ext4/xfs.
	Subvolumes []Subvolume
}

// Subvolume is one btrfs subvolume in a DiskLayout.
type Subvolume struct {
	// Name is the subvolume name as created, e.g. "@" or "@home".
	Name string
	// MountPoint is the guest-absolute path it is mounted at, e.g. "/" or "/home".
	// Exactly one entry must be "/" — it becomes the root and everything else nests
	// under it, so without it there is nothing to mount the others into.
	MountPoint string
	// MountOptions are extra comma-joined mount options, e.g. "compress=zstd,noatime".
	// `subvol=<Name>` is always prepended and must not be repeated here.
	MountOptions string
}

// parseDiskSizeBytes parses a `truncate -s` size suffix ("20G", "10240M",
// "536870912", "2GiB", "500MB") into a byte count, matching truncate's
// semantics: bare K/M/G/T/P are 1024-based, the "iB" forms are 1024-based,
// and the "B" forms (KB/MB/GB) are 1000-based. Used to compute the
// MinFreeBytes floor for the privileged disk build — the qcow2 output must
// fit on the staging + destination filesystems.
func parseDiskSizeBytes(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty disk size")
	}
	// Split the numeric prefix from the suffix.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid disk size %q: no numeric prefix", orig)
	}
	num, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid disk size %q: %w", orig, err)
	}
	suffix := s[i:]
	if suffix == "" {
		return num, nil
	}
	var mult int64
	switch strings.ToUpper(suffix) {
	case "K", "KIB":
		mult = 1 << 10
	case "M", "MIB":
		mult = 1 << 20
	case "G", "GIB":
		mult = 1 << 30
	case "T", "TIB":
		mult = 1 << 40
	case "P", "PIB":
		mult = 1 << 50
	case "KB":
		mult = 1000
	case "MB":
		mult = 1000 * 1000
	case "GB":
		mult = 1000 * 1000 * 1000
	case "TB":
		mult = 1000 * 1000 * 1000 * 1000
	default:
		return 0, fmt.Errorf("invalid disk size %q: unknown suffix %q", orig, suffix)
	}
	return num * mult, nil
}

// diskBuildScriptTmpl renders the partition + format + mount sequence for a fresh VM disk. The
// caller appends the rootfs install + chroot commands after `# >>> install rootfs <<<` and
// before the finalization (`# <<< install rootfs >>>`) markers.
//
// Layout (matches the standard Debian/Ubuntu installer layout):
//
//	/out/disk.raw         — sparse raw disk
//	/dev/loopX{p1,p2}     — ESP, root partitions (X varies)
//	{{.Mnt}}              — root partition mounted (ext4/xfs/btrfs).
//	                        /boot lives here too, so kernel images,
//	                        initramfs files, and their compatibility
//	                        symlinks (e.g. /boot/vmlinuz on Ubuntu)
//	                        land on a Unix-permission-aware filesystem.
//	{{.Mnt}}/boot/efi     — ESP mounted (FAT32). Only EFI binaries
//	                        (BOOTX64.EFI, grubx64.efi) live here.
//
// The script unmounts and detaches the loop device on exit (trap), then `qemu-img convert`
// produces /out/disk.qcow2.
const diskBuildScriptTmpl = `set -euo pipefail
mkdir -p /out
RAW=/out/disk.raw
truncate -s {{.SizeBytesOrSuffix}} "$RAW"
LOOP=$(losetup --find --show --partscan "$RAW")
trap '
  umount {{.Mnt}}{{.EspMountPoint}} 2>/dev/null || true
{{- range .Subvolumes}}{{if ne .MountPoint "/"}}
  umount {{$.Mnt}}{{.MountPoint}} 2>/dev/null || true
{{- end}}{{end}}
  umount {{.Mnt}} 2>/dev/null || true
  losetup -d "$LOOP" 2>/dev/null || true
' EXIT
parted -s "$LOOP" \
  mklabel gpt \
  mkpart ESP fat32 1MiB {{.EspSizeMib}}MiB \
  set 1 esp on \
  mkpart root {{.Rootfs}} {{.EspSizeMib}}MiB 100%
partprobe "$LOOP" || true
mkfs.fat -F32 "${LOOP}p1"
{{.MkfsCmd}} "${LOOP}p2"
mkdir -p {{.Mnt}}
{{- if .Subvolumes}}
# btrfs subvolume layout. The top level (subvolid=5) is mounted first ONLY to create the
# subvolumes, then unmounted -- a subvolume cannot be created through a mount that is
# already pinned to a different subvolume, and mounting the root subvolume directly would
# leave nowhere to create its siblings.
mount "${LOOP}p2" {{.Mnt}}
{{- range .Subvolumes}}
btrfs subvolume create {{$.Mnt}}/{{.Name}}
{{- end}}
umount {{.Mnt}}
{{- range .Subvolumes}}{{if eq .MountPoint "/"}}
mount -o subvol={{.Name}}{{if .MountOptions}},{{.MountOptions}}{{end}} "${LOOP}p2" {{$.Mnt}}
{{- end}}{{end}}
{{- range .Subvolumes}}{{if ne .MountPoint "/"}}
mkdir -p {{$.Mnt}}{{.MountPoint}}
mount -o subvol={{.Name}}{{if .MountOptions}},{{.MountOptions}}{{end}} "${LOOP}p2" {{$.Mnt}}{{.MountPoint}}
{{- end}}{{end}}
{{- else}}
mount "${LOOP}p2" {{.Mnt}}
{{- end}}
mkdir -p {{.Mnt}}{{.EspMountPoint}}
mount "${LOOP}p1" {{.Mnt}}{{.EspMountPoint}}
# >>> install rootfs <<<
`

// diskBuildFinalizeTmpl renders the unmount + qcow2 conversion tail. Combined with
// diskBuildScriptTmpl + caller-supplied install body to form the full bash body passed to
// RunPrivileged.
const diskBuildFinalizeTmpl = `# <<< install rootfs >>>
sync
umount {{.Mnt}}{{.EspMountPoint}}
{{- range .Subvolumes}}{{if ne .MountPoint "/"}}
umount {{$.Mnt}}{{.MountPoint}}
{{- end}}{{end}}
umount {{.Mnt}}
losetup -d "$LOOP"
trap - EXIT
qemu-img convert -O qcow2 "$RAW" /out/disk.qcow2
rm -f "$RAW"
`

// EmitDiskBuildScript renders the prelude (partition + format + mount) and finalize (unmount +
// qcow2 convert) halves of a privileged VM disk-build script. The caller stitches them around
// its own rootfs install body. Returns (prelude, finalize) on success.
func EmitDiskBuildScript(layout DiskLayout) (string, string, error) {
	if layout.Rootfs == "" {
		layout.Rootfs = "ext4"
	}
	if layout.EspSizeMib == 0 {
		layout.EspSizeMib = 512
	}
	if layout.Mnt == "" {
		layout.Mnt = "/mnt"
	}
	if layout.EspMountPoint == "" {
		layout.EspMountPoint = "/boot/efi"
	}
	if len(layout.Subvolumes) > 0 {
		if layout.Rootfs != "btrfs" {
			return "", "", fmt.Errorf("subvolumes require rootfs btrfs, got %q", layout.Rootfs)
		}
		roots := 0
		for _, sv := range layout.Subvolumes {
			if sv.Name == "" {
				return "", "", fmt.Errorf("subvolume with empty name")
			}
			if !strings.HasPrefix(sv.MountPoint, "/") {
				return "", "", fmt.Errorf("subvolume %q mount_point %q is not absolute", sv.Name, sv.MountPoint)
			}
			if sv.MountPoint == "/" {
				roots++
			}
		}
		if roots != 1 {
			return "", "", fmt.Errorf("exactly one subvolume must mount at \"/\", got %d", roots)
		}
	}
	mkfs := ""
	switch layout.Rootfs {
	case "ext4":
		mkfs = "mkfs.ext4 -F"
	case "xfs":
		mkfs = "mkfs.xfs -f"
	case "btrfs":
		mkfs = "mkfs.btrfs -f"
	default:
		return "", "", fmt.Errorf("unsupported rootfs %q (want ext4|xfs|btrfs)", layout.Rootfs)
	}
	ctx := struct {
		SizeBytesOrSuffix string
		Rootfs            string
		EspSizeMib        int
		Mnt               string
		MkfsCmd           string
		EspMountPoint     string
		Subvolumes        []Subvolume
	}{
		SizeBytesOrSuffix: layout.SizeBytesOrSuffix,
		Rootfs:            layout.Rootfs,
		EspSizeMib:        layout.EspSizeMib,
		Mnt:               layout.Mnt,
		MkfsCmd:           mkfs,
		EspMountPoint:     layout.EspMountPoint,
		Subvolumes:        layout.Subvolumes,
	}
	prelude, err := renderTmpl("disk-prelude", diskBuildScriptTmpl, ctx)
	if err != nil {
		return "", "", err
	}
	finalize, err := renderTmpl("disk-finalize", diskBuildFinalizeTmpl, ctx)
	if err != nil {
		return "", "", err
	}
	return prelude, finalize, nil
}

func renderTmpl(name, tmpl string, ctx any) (string, error) {
	t, err := template.New(name).Funcs(buildkit.TemplateFuncs).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}
