package vm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// libvirt_gpu_config.go — the two halves of the GPU configuration surface that are NOT a
// straight field-for-attribute mapping: the per-graphics-type <gl> rules, and the
// <qemu:override> seam.
//
// WHY THE RULES LIVE HERE AND NOT IN CUE. The natural home for "a <gl> block is meaningless
// on a vnc graphics device" is a cross-rule in #LibvirtGraphics. Written the obvious way —
// `if type == "vnc" { gl?: _|_ }` — it makes `cue exp gengotypes` emit
// `type LibvirtGraphics any`: the generator evaluates the definition with `type` still
// abstract, so every branch stays unresolved, the field kinds reduce to bottom, and it
// degrades the whole struct. That still compiles, so the damage is invisible — every
// graphics block would decode into an untyped map. (The `if firmware == …` rules on #Vm are
// safe only because they tighten fields to concrete VALUES, never to bottom.)
//
// So the rules run here, at the exact point where the field would otherwise be silently
// dropped, and where the message can say why.

// glGraphicsTypes are the graphics types libvirt gives a <gl> element at all.
var glGraphicsTypes = map[string]bool{"spice": true, "egl-headless": true, "dbus": true}

// validateLibvirtGpuConfig rejects a GPU configuration whose fields would render to
// nothing. Every check here guards a SILENT failure: without it the domain starts, the
// author's directive is absent from the XML, and the guest quietly gets different hardware
// than was asked for.
func validateLibvirtGpuConfig(lv *LibvirtDomain) error {
	if lv == nil {
		return nil
	}
	if lv.Devices != nil {
		for i, g := range lv.Devices.Graphics {
			if err := validateGraphicsGL(i, g); err != nil {
				return err
			}
		}
	}
	return validateQemuOverrideTargets(lv)
}

func validateGraphicsGL(i int, g LibvirtGraphics) error {
	if g.GL != nil && !glGraphicsTypes[g.Type] {
		return fmt.Errorf(
			"libvirt.devices.graphics[%d] (type %q) declares gl:, but libvirt defines a <gl> "+
				"element only for spice, egl-headless and dbus — it would render to nothing. "+
				"Remove gl:, or use one of those types", i, g.Type)
	}
	if g.Type == "egl-headless" && g.GL != nil && g.GL.Enable != nil {
		return fmt.Errorf(
			"libvirt.devices.graphics[%d] (type egl-headless) declares gl.enable, but "+
				"egl-headless' <gl> carries only rendernode= — libvirt defines no enable= "+
				"attribute for it, so the value would be dropped. Use gl.render_node", i)
	}
	if g.Type != "dbus" && (g.Address != "" || g.P2P != nil) {
		return fmt.Errorf(
			"libvirt.devices.graphics[%d] (type %q) declares address:/p2p:, which exist only "+
				"on <graphics type='dbus'>", i, g.Type)
	}
	// libvirt's RNG models dbus' address and p2p as a CHOICE of two single-attribute
	// groups, not as two independent optionals: p2p means peer-to-peer with no bus, so a
	// bus address alongside it is contradictory. libvirt rejects the domain outright.
	if g.Type == "dbus" && g.Address != "" && g.P2P != nil {
		return fmt.Errorf(
			"libvirt.devices.graphics[%d] declares both address: and p2p:, which libvirt "+
				"models as mutually exclusive — p2p is a direct peer-to-peer connection with "+
				"no bus address. Declare one", i)
	}
	// enable= is a REQUIRED attribute of <gl> on spice and dbus (optional only on
	// egl-headless, which has no enable at all). Emitting <gl rendernode=…/> without it
	// produces a domain libvirt refuses to define.
	if (g.Type == "spice" || g.Type == "dbus") && g.GL != nil && g.GL.Enable == nil {
		return fmt.Errorf(
			"libvirt.devices.graphics[%d] (type %q) declares gl: without gl.enable, but "+
				"libvirt requires the enable= attribute on a <gl> element for this type. "+
				"Set gl.enable: true", i, g.Type)
	}
	return nil
}

// validateQemuOverrideTargets rejects an override naming an alias no device declares.
//
// This is the whole reason the seam is keyed by alias rather than by device index: libvirt
// only accepts an override against a USER alias (`ua-`-prefixed), never its own
// auto-assigned `video0`. An override whose key matches no device is accepted by libvirt
// and applies to nothing at all — the single most likely authoring mistake, and completely
// silent.
func validateQemuOverrideTargets(lv *LibvirtDomain) error {
	if len(lv.QemuOverride) == 0 {
		return nil
	}
	declared := declaredDeviceAliases(lv)
	var unmatched []string
	for alias := range lv.QemuOverride {
		if !declared[alias] {
			unmatched = append(unmatched, alias)
		}
	}
	if len(unmatched) == 0 {
		return nil
	}
	sort.Strings(unmatched)
	known := make([]string, 0, len(declared))
	for a := range declared {
		known = append(known, a)
	}
	sort.Strings(known)
	have := "no device declares an alias:"
	if len(known) > 0 {
		have = "declared device aliases: " + strings.Join(known, ", ")
	}
	return fmt.Errorf(
		"libvirt.qemu_override targets %s, which no device declares — the override would "+
			"apply to nothing (%s). Set the matching alias: on the device, e.g. "+
			"libvirt.devices.video[0].alias",
		strings.Join(unmatched, ", "), have)
}

// declaredDeviceAliases collects every user alias the domain's devices declare.
func declaredDeviceAliases(lv *LibvirtDomain) map[string]bool {
	out := map[string]bool{}
	if lv.Devices == nil {
		return out
	}
	for _, v := range lv.Devices.Video {
		if v.Alias != "" {
			out[v.Alias] = true
		}
	}
	return out
}

// buildQemuOverride renders libvirt.qemu_override into the <qemu:override> namespace
// element. Returns nil when nothing is declared, so a domain that does not use the seam
// carries no qemu namespace at all and its XML is unchanged.
//
// Device and property order is SORTED, because Go map iteration is random and an XML that
// differs run to run breaks both golden tests and any diff a human reads.
func buildQemuOverride(lv *LibvirtDomain) *libvirtxml.DomainQEMUOverride {
	if lv == nil || len(lv.QemuOverride) == 0 {
		return nil
	}
	aliases := make([]string, 0, len(lv.QemuOverride))
	for a := range lv.QemuOverride {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)

	out := &libvirtxml.DomainQEMUOverride{}
	for _, alias := range aliases {
		props := lv.QemuOverride[alias]
		names := make([]string, 0, len(props))
		for n := range props {
			names = append(names, n)
		}
		sort.Strings(names)
		dev := libvirtxml.DomainQEMUOverrideDevice{Alias: alias}
		for _, n := range names {
			typ, val := qemuPropertyValue(props[n])
			dev.Frontend.Properties = append(dev.Frontend.Properties,
				libvirtxml.DomainQEMUOverrideProperty{Name: n, Type: typ, Value: val})
		}
		out.Devices = append(out.Devices, dev)
	}
	return out
}

// qemuPropertyValue renders one property value plus the type= attribute libvirt's
// <qemu:property> requires. The type attribute is NOT cosmetic: QEMU parses the value
// according to it, so a bool emitted as a string is a different setting from the one the
// author wrote.
//
// The vocabulary is fixed by libvirt's own RNG (domaincommon.rng, `qemuoverrideproperty`):
//
//	string | signed | unsigned | bool | remove
//
// There is NO "number" — emitting it makes libvirt reject the whole domain at define time,
// with an error that blames <frontend> rather than the property. An integer is `unsigned`
// when it is non-negative and `signed` otherwise.
func qemuPropertyValue(v any) (typ, val string) {
	switch t := v.(type) {
	case bool:
		return "bool", strconv.FormatBool(t)
	case int:
		return intPropertyType(int64(t)), strconv.Itoa(t)
	case int64:
		return intPropertyType(t), strconv.FormatInt(t, 10)
	case uint64:
		return "unsigned", strconv.FormatUint(t, 10)
	case float64:
		// YAML decodes an unquoted integer as float64 through some paths; render it
		// without a spurious ".0", which QEMU would reject for an integer property.
		if t == float64(int64(t)) {
			return intPropertyType(int64(t)), strconv.FormatInt(int64(t), 10)
		}
		// A genuinely fractional value has no libvirt property type; string is the only
		// honest carrier, and QEMU parses it per the device's own property definition.
		return "string", strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return "string", t
	default:
		return "string", fmt.Sprint(t)
	}
}

func intPropertyType(n int64) string {
	if n < 0 {
		return "signed"
	}
	return "unsigned"
}

// yesNoOrOmit maps a tri-state bool to libvirt's yes/no attribute, rendering "" — which
// libvirtxml's `omitempty` drops — when the field was never set.
//
// It exists because the established boolPtrToYesNo collapses nil into "no", documented as
// matching the legacy renderer. That is correct for the attributes that already shipped
// (primary=, accel3d=): their XML has always carried an explicit "no", and changing it now
// would change every existing domain. It is WRONG for every attribute added by this cutover.
// Using it for blob=/edid=/accel2d= stamped blob="no" edid="no" accel2d="no" onto every VM in
// the corpus — caught by TestRenderDomainXML_ArchCloudBase, whose exact-fragment match on
// <acceleration accel3d="no"> broke when accel2d appeared beside it.
//
// The distinction is real, not cosmetic: an omitted attribute takes libvirt's own default,
// while an explicit "no" pins it. For blob those are the same today and need not stay so.
func yesNoOrOmit(p *bool) string {
	if p == nil {
		return ""
	}
	if *p {
		return "yes"
	}
	return "no"
}
