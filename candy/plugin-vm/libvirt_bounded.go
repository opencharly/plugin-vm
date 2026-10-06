package vm

// libvirt_bounded.go — bounded wrappers for the `verb:libvirt` INTROSPECTION RPCs.
//
// The #800 fix (plugin-vm#70) and its completion (plugin-vm#74) bounded every
// context-less libvirt RPC the `charly vm create/start/stop/destroy` lifecycle
// path reaches (the seams in vm_libvirt.go). The `charly check libvirt …` verb
// surface (libvirt_cmd.go / libvirt_ops.go) and the provider's introspection ops
// (provider.go) reach the SAME class of context-less, timeout-less go-libvirt
// RPCs — go-libvirt sets no socket deadline anywhere — so a wedged virtqemud
// blocks any `libvirt:` check verb forever, exactly as it used to hang
// `charly vm destroy`. This file bounds them (plugin-vm#73).
//
// One bound per RPC, each a boundedRPC/boundedRPCValue call around a `raw*` seam
// (the vm_libvirt.go pattern: package vars so a test can substitute a BLOCKING raw
// call and prove the CALL SITE returns at the bound). The introspection verbs are
// interactive probes with no teardown deadline of their own, so they share the
// generous teardownRPCBound via libvirtProbeBound (a var, so a test can lower it).
//
// DomainScreenshot IS bounded here (the `screenshot` method) — but at its OWN
// screenshotBound, deliberately smaller than the recorder's frame interval. Because
// it is a go-libvirt STREAM call, each timeout abandons one blocked goroutine, so a
// wedged daemon must not be re-polled frame after frame; the recorder's frame path
// (recorder.go) provides that half — writeFrames stops after
// recorderConsecutiveFailLimit consecutive screenshot failures instead of polling
// forever.

import (
	"io"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// libvirtProbeBound is the wall-clock bound for a single introspection RPC. A VAR
// so the call-site guard test can lower it and assert the bound in milliseconds.
var libvirtProbeBound = teardownRPCBound

// The raw seams — package vars so a test can substitute a blocking call. Each
// defaults to the real go-libvirt method (the vm_libvirt.go pattern).
var (
	rawConnectListAllDomains = func(l *libvirt.Libvirt, needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error) {
		return l.ConnectListAllDomains(needResults, flags)
	}
	rawDomainGetInfo = func(l *libvirt.Libvirt, d libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
		return l.DomainGetInfo(d)
	}
	rawDomainGetXMLFlags = func(l *libvirt.Libvirt, d libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
		return l.DomainGetXMLDesc(d, flags)
	}
	rawDomainSendKey = func(l *libvirt.Libvirt, d libvirt.Domain, codeset, holdtime uint32, keycodes []uint32, flags uint32) error {
		return l.DomainSendKey(d, codeset, holdtime, keycodes, flags)
	}
	rawDomainUpdateDeviceFlags = func(l *libvirt.Libvirt, d libvirt.Domain, xml string, flags libvirt.DomainDeviceModifyFlags) error {
		return l.DomainUpdateDeviceFlags(d, xml, flags)
	}
	rawQEMUDomainMonitorCommand = func(l *libvirt.Libvirt, d libvirt.Domain, cmd string, flags uint32) (string, error) {
		return l.QEMUDomainMonitorCommand(d, cmd, flags)
	}
	rawDomainListAllSnapshots = func(l *libvirt.Libvirt, d libvirt.Domain, needResults int32, flags uint32) ([]libvirt.DomainSnapshot, int32, error) {
		return l.DomainListAllSnapshots(d, needResults, flags)
	}
	rawDomainSnapshotCreateXML = func(l *libvirt.Libvirt, d libvirt.Domain, xml string, flags uint32) (libvirt.DomainSnapshot, error) {
		return l.DomainSnapshotCreateXML(d, xml, flags)
	}
	rawDomainSnapshotLookupByName = func(l *libvirt.Libvirt, d libvirt.Domain, name string, flags uint32) (libvirt.DomainSnapshot, error) {
		return l.DomainSnapshotLookupByName(d, name, flags)
	}
	rawDomainSnapshotGetXMLDesc = func(l *libvirt.Libvirt, s libvirt.DomainSnapshot, flags uint32) (string, error) {
		return l.DomainSnapshotGetXMLDesc(s, flags)
	}
	rawDomainRevertToSnapshot = func(l *libvirt.Libvirt, s libvirt.DomainSnapshot, flags uint32) error {
		return l.DomainRevertToSnapshot(s, flags)
	}
	rawDomainSnapshotDelete = func(l *libvirt.Libvirt, s libvirt.DomainSnapshot, flags libvirt.DomainSnapshotDeleteFlags) error {
		return l.DomainSnapshotDelete(s, flags)
	}
	rawQEMUDomainAgentCommand = func(l *libvirt.Libvirt, d libvirt.Domain, cmd string, timeout int32, flags uint32) (libvirt.OptString, error) {
		return l.QEMUDomainAgentCommand(d, cmd, timeout, flags)
	}
	rawDomainScreenshot = func(l *libvirt.Libvirt, d libvirt.Domain, w io.Writer, screen uint32, flags uint32) (libvirt.OptString, error) {
		return l.DomainScreenshot(d, w, screen, flags)
	}
)

// listAllDomains is the bounded form of ConnectListAllDomains (libvirt list,
// the graphics/status collectors, and the provider's list-all-domains op).
func (c *libvirtConn) listAllDomains(needResults int32, flags libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, error) {
	return boundedRPCValue("list domains", libvirtProbeBound, func() ([]libvirt.Domain, error) {
		doms, _, err := rawConnectListAllDomains(c.l, needResults, flags)
		return doms, err
	})
}

// domainInfo is the bounded form of DomainGetInfo.
func (c *libvirtConn) domainInfo(dom libvirt.Domain) (state uint8, maxMem, memory uint64, nrCPU uint16, cpuTime uint64, err error) {
	type info struct {
		state   uint8
		maxMem  uint64
		memory  uint64
		nrCPU   uint16
		cpuTime uint64
	}
	v, err := boundedRPCValue("domain info", libvirtProbeBound, func() (info, error) {
		st, mm, mem, nc, ct, e := rawDomainGetInfo(c.l, dom)
		return info{st, mm, mem, nc, ct}, e
	})
	return v.state, v.maxMem, v.memory, v.nrCPU, v.cpuTime, err
}

// sendKey is the bounded form of DomainSendKey.
func (c *libvirtConn) sendKey(dom libvirt.Domain, codeset, holdtime uint32, keycodes []uint32, flags uint32) error {
	return boundedRPC("send key", libvirtProbeBound, func() error {
		return rawDomainSendKey(c.l, dom, codeset, holdtime, keycodes, flags)
	})
}

// updateDeviceFlags is the bounded form of DomainUpdateDeviceFlags (the live
// graphics-password patch).
func (c *libvirtConn) updateDeviceFlags(dom libvirt.Domain, xmlStr string, flags libvirt.DomainDeviceModifyFlags) error {
	return boundedRPC("update device flags", libvirtProbeBound, func() error {
		return rawDomainUpdateDeviceFlags(c.l, dom, xmlStr, flags)
	})
}

// domainXML is the bounded form of DomainGetXMLDesc for the VERB path (which needs
// arbitrary flags; vm_libvirt.go's getDomainXML hardcodes 0 for the lifecycle path).
func (c *libvirtConn) domainXML(dom libvirt.Domain, flags libvirt.DomainXMLFlags) (string, error) {
	return boundedRPCValue("domain xml", libvirtProbeBound, func() (string, error) {
		return rawDomainGetXMLFlags(c.l, dom, flags)
	})
}

// qmpCommand is the bounded form of QEMUDomainMonitorCommand.
func (c *libvirtConn) qmpCommand(dom libvirt.Domain, cmd string, flags uint32) (string, error) {
	return boundedRPCValue("qmp command", libvirtProbeBound, func() (string, error) {
		return rawQEMUDomainMonitorCommand(c.l, dom, cmd, flags)
	})
}

// listSnapshots is the bounded form of DomainListAllSnapshots.
func (c *libvirtConn) listSnapshots(dom libvirt.Domain) ([]libvirt.DomainSnapshot, error) {
	return boundedRPCValue("snapshot list", libvirtProbeBound, func() ([]libvirt.DomainSnapshot, error) {
		snaps, _, err := rawDomainListAllSnapshots(c.l, dom, 1, 0)
		return snaps, err
	})
}

// snapshotCreateXML is the bounded form of DomainSnapshotCreateXML.
func (c *libvirtConn) snapshotCreateXML(dom libvirt.Domain, xmlStr string, flags uint32) (libvirt.DomainSnapshot, error) {
	return boundedRPCValue("snapshot create", libvirtProbeBound, func() (libvirt.DomainSnapshot, error) {
		return rawDomainSnapshotCreateXML(c.l, dom, xmlStr, flags)
	})
}

// snapshotLookupByName is the bounded form of DomainSnapshotLookupByName.
func (c *libvirtConn) snapshotLookupByName(dom libvirt.Domain, name string) (libvirt.DomainSnapshot, error) {
	return boundedRPCValue("snapshot lookup", libvirtProbeBound, func() (libvirt.DomainSnapshot, error) {
		return rawDomainSnapshotLookupByName(c.l, dom, name, 0)
	})
}

// snapshotXMLDesc is the bounded form of DomainSnapshotGetXMLDesc.
func (c *libvirtConn) snapshotXMLDesc(snap libvirt.DomainSnapshot) (string, error) {
	return boundedRPCValue("snapshot xml", libvirtProbeBound, func() (string, error) {
		return rawDomainSnapshotGetXMLDesc(c.l, snap, 0)
	})
}

// revertToSnapshot is the bounded form of DomainRevertToSnapshot.
func (c *libvirtConn) revertToSnapshot(snap libvirt.DomainSnapshot) error {
	return boundedRPC("snapshot revert", libvirtProbeBound, func() error {
		return rawDomainRevertToSnapshot(c.l, snap, 0)
	})
}

// snapshotDeleteByHandle is the bounded form of DomainSnapshotDelete.
func (c *libvirtConn) snapshotDeleteByHandle(snap libvirt.DomainSnapshot) error {
	return boundedRPC("snapshot delete", libvirtProbeBound, func() error {
		return rawDomainSnapshotDelete(c.l, snap, snapshotDeleteFlags())
	})
}

// screenshotBound is the wall-clock bound for one DomainScreenshot stream. A var so
// the guard test can lower it. Deliberately below the recorder's frame interval
// (default 2s) so a wedged stream is DROPPED, not serialized frame-after-frame.
var screenshotBound = 1500 * time.Millisecond

// screenshot streams one framebuffer capture, bounded. DomainScreenshot is a
// go-libvirt STREAM call; on timeout it returns a NAMED error WITHOUT waiting for
// the underlying call (see boundedRPCValue). Callers that poll (the session
// recorder) MUST treat the error as terminal — each timeout abandons one blocked
// goroutine, so a wedged daemon must stop the recorder rather than be re-polled.
func (c *libvirtConn) screenshot(dom libvirt.Domain, w io.Writer, screen uint32, flags uint32) (libvirt.OptString, error) {
	return boundedRPCValue("domain screenshot", screenshotBound, func() (libvirt.OptString, error) {
		return rawDomainScreenshot(c.l, dom, w, screen, flags)
	})
}

// agentCommand is the bounded, package-level form of QEMUDomainAgentCommand — the
// qemu-guest-agent RPC. The guest agent has its OWN in-band timeout (a.to, seconds),
// but go-libvirt still wraps it in a context-less request: a daemon that accepts the
// connection and then wedges never returns it. The wall-clock bound is the agent's
// own timeout PLUS a margin, so a healthy agent is never cut off early yet a wedged
// virtqemud fails at a known point.
func agentCommand(l *libvirt.Libvirt, dom libvirt.Domain, cmd string, timeoutSec int32, flags uint32) (libvirt.OptString, error) {
	bound := time.Duration(timeoutSec)*time.Second + libvirtProbeBound
	return boundedRPCValue("guest agent command", bound, func() (libvirt.OptString, error) {
		return rawQEMUDomainAgentCommand(l, dom, cmd, timeoutSec, flags)
	})
}

// agentCommand is the method form (a GuestAgent's connection + default timeout).
func (a *GuestAgent) agentCommand(cmd string, flags uint32) (libvirt.OptString, error) {
	return agentCommand(a.l, a.d, cmd, a.to, flags)
}
