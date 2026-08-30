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
	// BOTH cdroms. The answers volume is what stops the installer prompting; dropping it
	// leaves an installer sitting at its first question with nobody at the keyboard.
	if !strings.Contains(out, "/state/omarchy-vm/seed.iso") {
		t.Errorf("the answers volume was dropped when an installer was present; got:\n%s", out)
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

	// Distinct targets, or libvirt refuses the domain outright.
	if strings.Count(out, `dev="sda"`) != 1 || strings.Count(out, `dev="sdb"`) != 1 {
		t.Errorf("the two cdroms must have distinct targets (sda, sdb); got:\n%s", out)
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
}
