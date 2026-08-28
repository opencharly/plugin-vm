package vm

import (
	"strings"
	"testing"
)

func bp(b bool) *bool { return &b }

func gpuSpec(lv *LibvirtDomain) *VmSpec {
	return &VmSpec{
		Source:  VmSource{Kind: "cloud_image", URL: "http://x/y.qcow2"},
		Libvirt: lv,
	}
}

var gpuRt = VmRuntimeParams{Name: "charly-gpu", RamMB: 8192, Cpus: 4, HostArch: "x86_64"}

// The configuration Spike 3 proved works, expressed entirely in charly YAML instead of the
// hand-injected <qemu:override> the spike needed. Every fragment below was unreachable
// before this cutover.
func TestRenderDomainXML_BlobNativeContext(t *testing.T) {
	xmlOut, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
		Devices: &LibvirtDevices{
			Video: []LibvirtVideo{{
				Model: "virtio", Device: "virtio-gpu-gl", Blob: bp(true),
				Heads: 1, Accel3D: bp(true), Alias: "ua-gpu",
			}},
			Graphics: []LibvirtGraphics{{
				Type: "egl-headless",
				GL:   &LibvirtGraphicsGL{RenderNode: "/dev/dri/renderD128"},
			}},
		},
		QemuOverride: map[string]map[string]any{
			"ua-gpu": {"drm_native_context": true, "hostmem": "4G", "max_outputs": 1},
		},
	}), gpuRt)
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}
	for _, want := range []string{
		// device= is what selects a GL-capable device; model="virtio" alone is plain
		// virtio-vga, which has no GL and therefore no blob support.
		`device="virtio-gpu-gl"`,
		`blob="on"`,
		`<alias name="ua-gpu">`,
		// egl-headless' <gl> carries rendernode only.
		`<gl rendernode="/dev/dri/renderD128">`,
		// The override — the only path to these knobs, since libvirt models no element.
		//
		// Go's encoder spells the qemu namespace as a DEFAULT xmlns on <override>
		// rather than as the `qemu:` prefix libvirt's docs show. Both are the same
		// namespace to any namespace-aware parser, and the rendered domain validates
		// against libvirt 12.6.0's own RNG (see the PR's virt-xml-validate run), so the
		// assertion is on the namespace URI and the structure — never on a prefix
		// spelling the encoder does not control.
		`<override xmlns="http://libvirt.org/schemas/domain/qemu/1.0">`,
		`<device alias="ua-gpu">`,
		`<property name="drm_native_context" type="bool" value="true">`,
		`<property name="hostmem" type="string" value="4G">`,
		`<property name="max_outputs" type="unsigned" value="1">`,
		// blob REQUIRES shared memory backing; libvirt refuses to start without it, so
		// the renderer auto-pairs it exactly as it does for virtiofs.
		`<source type="memfd">`,
		`<access mode="shared">`,
	} {
		if !strings.Contains(xmlOut, want) {
			t.Errorf("domain XML missing %q\n---\n%s", want, xmlOut)
		}
	}
}

// The byte-identity bound: a video device declaring only what candies declare TODAY must
// render exactly the attributes it rendered before this cutover — no blob="no", no
// edid="no", no accel2d="no".
//
// This is the assertion that caught the real bug: the established boolPtrToYesNo collapses
// nil into "no", so wiring the new tri-state fields through it stamped three extra
// attributes onto every VM in the corpus.
func TestRenderDomainXML_UnsetTriStatesAreOmitted(t *testing.T) {
	xmlOut, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
		Devices: &LibvirtDevices{
			Video: []LibvirtVideo{{Model: "virtio", VRAM: 65536, Heads: 1, Accel3D: bp(false)}},
		},
	}), gpuRt)
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}
	// What the pre-cutover renderer emitted, unchanged.
	if !strings.Contains(xmlOut, `<acceleration accel3d="no">`) {
		t.Errorf("the legacy accel3d=\"no\" spelling changed\n---\n%s", xmlOut)
	}
	// What must NOT appear: an attribute the author never set.
	for _, forbidden := range []string{`blob=`, `edid=`, `accel2d=`, `rendernode=`, `<alias`, `qemu:override`} {
		if strings.Contains(xmlOut, forbidden) {
			t.Errorf("unset field %q leaked into the XML of an ordinary VM\n---\n%s", forbidden, xmlOut)
		}
	}
}

// Each of these would otherwise render to NOTHING: the field is accepted, dropped, and the
// domain starts with hardware the author did not ask for. They are render errors precisely
// because CUE cannot express them without degrading LibvirtGraphics to `any`.
func TestRenderDomainXML_GraphicsGLRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    LibvirtGraphics
		want string
	}{
		{"gl on vnc has no element", LibvirtGraphics{Type: "vnc", GL: &LibvirtGraphicsGL{Enable: bp(true)}},
			"only for spice, egl-headless and dbus"},
		{"gl on sdl has no element", LibvirtGraphics{Type: "sdl", GL: &LibvirtGraphicsGL{RenderNode: "/dev/dri/renderD128"}},
			"only for spice, egl-headless and dbus"},
		{"egl-headless gl has no enable attribute", LibvirtGraphics{Type: "egl-headless", GL: &LibvirtGraphicsGL{Enable: bp(true)}},
			"no enable= attribute"},
		{"address is dbus-only", LibvirtGraphics{Type: "spice", Address: "unix:path=/x"},
			"only on <graphics type='dbus'>"},
		{"dbus address and p2p are mutually exclusive",
			LibvirtGraphics{Type: "dbus", Address: "unix:path=/x", P2P: bp(true), GL: &LibvirtGraphicsGL{Enable: bp(true)}},
			"mutually exclusive"},
		{"spice gl needs enable", LibvirtGraphics{Type: "spice", GL: &LibvirtGraphicsGL{RenderNode: "/dev/dri/renderD128"}},
			"requires the enable= attribute"},
		{"dbus gl needs enable", LibvirtGraphics{Type: "dbus", GL: &LibvirtGraphicsGL{RenderNode: "/dev/dri/renderD128"}},
			"requires the enable= attribute"},
		{"p2p is dbus-only", LibvirtGraphics{Type: "vnc", P2P: bp(true)},
			"only on <graphics type='dbus'>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
				Devices: &LibvirtDevices{Graphics: []LibvirtGraphics{tc.g}},
			}), gpuRt)
			if err == nil {
				t.Fatalf("expected a render error, got none")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the cause (%q)", err, tc.want)
			}
		})
	}
}

// The single most likely authoring mistake, and completely silent otherwise: libvirt accepts
// an override against an alias no device carries, and it applies to nothing at all.
func TestRenderDomainXML_OverrideMustMatchADeclaredAlias(t *testing.T) {
	_, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
		Devices: &LibvirtDevices{
			Video: []LibvirtVideo{{Model: "virtio", Alias: "ua-gpu"}},
		},
		QemuOverride: map[string]map[string]any{"ua-typo": {"blob": true}},
	}), gpuRt)
	if err == nil {
		t.Fatal("an override targeting an undeclared alias was accepted")
	}
	// The message must name BOTH the unmatched key and what is actually available —
	// "invalid alias" alone leaves the author guessing at a typo.
	for _, want := range []string{"ua-typo", "ua-gpu", "would apply to nothing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// The valid case must NOT error — otherwise the check above would pass by rejecting
// everything, which is the classic way a validation test becomes vacuous.
func TestRenderDomainXML_OverrideWithMatchingAliasIsAccepted(t *testing.T) {
	if _, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
		Devices:      &LibvirtDevices{Video: []LibvirtVideo{{Model: "virtio", Alias: "ua-gpu"}}},
		QemuOverride: map[string]map[string]any{"ua-gpu": {"blob": true}},
	}), gpuRt); err != nil {
		t.Fatalf("a correctly-targeted override was rejected: %v", err)
	}
}

// The full video surface reaches the XML — the fields that had no expression at all before.
func TestRenderDomainXML_FullVideoSurface(t *testing.T) {
	xmlOut, err := RenderDomainXML(gpuSpec(&LibvirtDomain{
		Devices: &LibvirtDevices{Video: []LibvirtVideo{{
			Model: "qxl", Ram: 65536, VRAM: 65536, VRAM64: 65536, VGAMem: 16384,
			Heads: 2, EDID: bp(true), Accel2D: bp(true),
			Resolution: &LibvirtVideoResolution{X: 1920, Y: 1080},
			Driver:     &LibvirtVideoDriver{Name: "qemu", VGAConf: "io", IOMMU: bp(true), ATS: bp(false)},
		}}},
	}), gpuRt)
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}
	for _, want := range []string{
		`ram="65536"`, `vram64="65536"`, `vgamem="16384"`, `edid="on"`,
		`accel2d="yes"`, `<resolution x="1920" y="1080">`,
		// The virtio <driver> toggles are on/off where the model's own attributes are
		// yes/no — libvirt rejects the wrong spelling, so the two are separate helpers.
		`<driver name="qemu" vgaconf="io" iommu="on" ats="off">`,
	} {
		if !strings.Contains(xmlOut, want) {
			t.Errorf("domain XML missing %q\n---\n%s", want, xmlOut)
		}
	}
}
