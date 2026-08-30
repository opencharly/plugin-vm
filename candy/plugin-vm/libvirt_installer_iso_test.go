package vm

import (
	"strings"
	"testing"
)

// TestRenderDomainXML_InstallerIsoSecondCdromAndBootOrder proves the libvirt half of the
// unattended-install mechanism: a second cdrom for the installer, and boot devices ordered
// DISK FIRST.
//
// The boot order is not a preference, it is what makes the install terminate. An empty disk
// is not bootable, so firmware falls through to the installer on the first boot; once the
// installer has written the disk, the disk boots and the ISO is never reached again.
// Nothing detects the end of the install and nothing is ejected. Reversed, the guest would
// reinstall over itself on every boot.
func TestRenderDomainXML_InstallerIsoSecondCdromAndBootOrder(t *testing.T) {
	spec := &VmSpec{Firmware: "uefi-insecure", Machine: "q35"}
	rt := VmRuntimeParams{
		Name:             "charly-omarchy",
		RamMB:            8192,
		Cpus:             4,
		QCOW2Path:        "/state/omarchy-vm/disk.qcow2",
		SeedISOPath:      "/state/omarchy-vm/seed.iso",
		InstallerISOPath: "/cache/omarchy-4.0.1.iso",
	}
	out, err := RenderDomainXML(spec, rt)
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}

	if !strings.Contains(out, "/cache/omarchy-4.0.1.iso") {
		t.Errorf("the installer ISO was not attached; got:\n%s", out)
	}
	// BOTH volumes. The answers volume is what stops the installer prompting; dropping it
	// leaves an installer sitting at its first question with nobody at the keyboard.
	if !strings.Contains(out, "/state/omarchy-vm/seed.iso") {
		t.Errorf("the answers volume was dropped when an installer was present; got:\n%s", out)
	}
	// And it must be a VIRTIO DISK, not a cdrom. An installer reads its answers by
	// filesystem label from a script that runs early on tty1; Omarchy's
	// omarchy-cidata-load calls `udevadm settle` — which drains only the queue as it
	// stands — and then reads /dev/disk/by-label/cidata, so a SATA cdrom whose probe has
	// not been QUEUED yet is missed and the script falls back to the interactive wizard.
	// Measured: as a cdrom the disk stayed at 197,248 bytes indefinitely; as a virtio disk
	// the same seed installed unattended to 6,092,816,384 bytes with no intervention.
	if !strings.Contains(out, `<target dev="vdb" bus="virtio"`) {
		t.Errorf("the answers volume must be a virtio disk (vdb) for an installer VM; got:\n%s", out)
	}

	// Disk BEFORE cdrom in the boot device list.
	hd := strings.Index(out, `<boot dev="hd"`)
	cd := strings.Index(out, `<boot dev="cdrom"`)
	if hd < 0 {
		t.Fatalf("no hd boot device emitted; got:\n%s", out)
	}
	if cd < 0 {
		t.Fatalf("an installer ISO must add a cdrom boot device; got:\n%s", out)
	}
	if hd > cd {
		t.Fatalf("boot order is cdrom-first — the guest would reinstall over itself on every boot; got:\n%s", out)
	}

	// The installer is the only SATA device now that the answers moved to virtio, so it
	// takes sda and there is no sdb gap.
	if strings.Count(out, `dev="sda"`) != 1 {
		t.Errorf("the installer cdrom must be the single sata device (sda); got:\n%s", out)
	}
	if strings.Contains(out, `dev="sdb"`) {
		t.Errorf("nothing should occupy sdb once the answers volume is on virtio; got:\n%s", out)
	}
}

// No installer means no change: exactly one boot device (hd) and one cdrom (the cidata
// seed). A boot-order change leaking into every existing cloud_image / bootc / bootstrap VM
// would be a serious regression hiding inside a feature addition.
func TestRenderDomainXML_NoInstallerLeavesBootDevicesAlone(t *testing.T) {
	spec := &VmSpec{Firmware: "uefi-insecure", Machine: "q35"}
	rt := VmRuntimeParams{
		Name:        "charly-arch",
		RamMB:       4096,
		Cpus:        2,
		QCOW2Path:   "/state/arch-vm/disk.qcow2",
		SeedISOPath: "/state/arch-vm/seed.iso",
	}
	out, err := RenderDomainXML(spec, rt)
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}
	if strings.Contains(out, `<boot dev="cdrom"`) {
		t.Errorf("a VM with no installer must not gain a cdrom boot device; got:\n%s", out)
	}
	if strings.Count(out, "<boot dev=") != 1 {
		t.Errorf("want exactly one boot device; got:\n%s", out)
	}
	if !strings.Contains(out, "/state/arch-vm/seed.iso") {
		t.Errorf("the cidata seed cdrom regressed; got:\n%s", out)
	}
	// Still a CDROM on sata, specifically. Moving cloud_image/bootc/bootstrap seeds to
	// virtio would change every existing VM: cloud-init finds a NoCloud source on either
	// bus and has never raced here, so there is nothing to gain and a fleet to regress.
	if !strings.Contains(out, `<target dev="sda" bus="sata"`) {
		t.Errorf("a non-installer VM's seed must stay a sata cdrom; got:\n%s", out)
	}
	if strings.Contains(out, `dev="vdb"`) {
		t.Errorf("a non-installer VM's seed moved to virtio — that changes every existing VM:\n%s", out)
	}
}
