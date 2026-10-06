package vm

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"
	"github.com/opencharly/sdk/vmshared"
	"golang.org/x/crypto/ssh"
)

// domainStateRunning is the libvirt domain state for a running VM.
const domainStateRunning = libvirt.DomainRunning

// libvirtConn wraps a go-libvirt connection to the session daemon.
// When the URI is qemu+ssh://, Tunnel holds the SSH client that
// forwards the remote virtqemud socket; Close tears everything down.
type libvirtConn struct {
	l      *libvirt.Libvirt
	tunnel *SSHTunnel // non-nil when connected via qemu+ssh://
	uri    LibvirtURI
}

// connectLibvirt connects to a libvirt session daemon — local by
// default, or remote when the URI is qemu+ssh://host/session.
//
// Empty uri is equivalent to "qemu:///session" (local). Local mode
// dials the virtqemud UNIX socket under $XDG_RUNTIME_DIR/libvirt/.
// Remote mode opens an SSH connection, discovers the remote user's
// virtqemud socket path over that SSH channel, forwards the socket
// into a local net.Conn, and speaks libvirt RPC through it.
//
// Uses ConnectToURI(qemu:///session) in all cases — the URI here is
// what the daemon connects to, not the transport. Modern libvirt
// ships per-driver modular daemons (virtqemud, virtnetworkd, …) and
// the session-scoped virtqemud only accepts /session URIs.
func connectLibvirt(uri string) (*libvirtConn, error) {
	parsed, err := ParseLibvirtURI(uri)
	if err != nil {
		return nil, err
	}
	if parsed.IsLocal() {
		return connectLocalLibvirtSession(parsed)
	}
	return connectRemoteLibvirtSession(parsed)
}

// connectLocalLibvirtSession dials the local virtqemud UNIX socket.
//
// Best-effort starts virtqemud.service (with libvirtd.service as a
// legacy fallback) before dialing — modular libvirt's `--timeout=120`
// causes the daemon to auto-exit after 120 s of idle, so consecutive
// `charly check libvirt …` invocations spaced wider than that find the
// socket gone. systemctl auto-restart on socket activation usually
// covers this, but on hosts without socket activation (no
// virtqemud.socket unit) the daemon stays down. Auto-starting here
// makes `charly check libvirt` self-healing on idle-timeout. See the
// 2026-05-06 R10 follow-up RCA.
func connectLocalLibvirtSession(parsed LibvirtURI) (*libvirtConn, error) {
	vmshared.StartLibvirtUserSession()
	sockPath := libvirtSessionSocket()
	c, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connecting to libvirt session socket %s: %w", sockPath, err)
	}
	l := libvirt.NewWithDialer(dialers.NewAlreadyConnected(c))
	if err := l.ConnectToURI(libvirt.QEMUSession); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("libvirt handshake failed: %w", err)
	}
	return &libvirtConn{l: l, uri: parsed}, nil
}

// connectRemoteLibvirtSession opens an SSH connection and forwards
// the remote virtqemud session socket. Socket path is discovered by
// running `id -u` over the SSH channel (remote $XDG_RUNTIME_DIR may
// not match the connecting user's UID if id remapping is in play,
// so using `id -u` is the robust choice).
func connectRemoteLibvirtSession(parsed LibvirtURI) (*libvirtConn, error) {
	tunnel, err := NewSSHTunnel(parsed.Remote)
	if err != nil {
		return nil, fmt.Errorf("ssh to %s: %w", parsed.Remote, err)
	}
	sockPath, err := remoteVirtqemudSocketPath(tunnel.Client())
	if err != nil {
		_ = tunnel.Close()
		return nil, fmt.Errorf("discovering remote virtqemud socket: %w", err)
	}
	conn, err := tunnel.Client().Dial("unix", sockPath)
	if err != nil {
		_ = tunnel.Close()
		return nil, fmt.Errorf("dialing remote socket %s via ssh: %w", sockPath, err)
	}
	l := libvirt.NewWithDialer(dialers.NewAlreadyConnected(conn))
	if err := l.ConnectToURI(libvirt.QEMUSession); err != nil {
		_ = conn.Close()
		_ = tunnel.Close()
		return nil, fmt.Errorf("libvirt handshake over ssh failed: %w", err)
	}
	return &libvirtConn{l: l, tunnel: tunnel, uri: parsed}, nil
}

// Close disconnects from libvirt, and from SSH if the connection was
// remote.
func (c *libvirtConn) Close() error {
	err := c.l.Disconnect()
	if c.tunnel != nil {
		if terr := c.tunnel.Close(); terr != nil && err == nil {
			err = terr
		}
	}
	return err
}

// boundedRPCValue is boundedRPC for a call that returns a value. An on-timeout
// RPC yields the zero value + the same NAMED error, so a wedged libvirt fails
// fast instead of blocking forever.
func boundedRPCValue[T any](op string, d time.Duration, fn func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	done := make(chan res, 1)
	go func() { v, err := fn(); done <- res{v, err} }()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(d):
		var zero T
		return zero, fmt.Errorf("%s: libvirt did not respond within %s (a wedged libvirt/qemu; the domain may be left running — re-check with `virsh -c qemu:///session list --all`)", op, d)
	}
}

// boundedRPC runs a libvirt RPC (which is context-less in go-libvirt, so it has no
// request timeout of its own) with a hard wall-clock bound. On timeout it returns a
// NAMED error WITHOUT waiting for the underlying call: a wedged virtqemud blocks the
// call forever, and waiting for it is exactly the hang this closes (a `charly vm
// destroy` that never returned and resisted SIGTERM — opencharly/charly#800). The
// abandoned goroutine is harmless: the process is exiting (teardown) or the caller
// has already decided to move on; and it cannot deadlock the process, unlike the
// unbounded call it replaces.
func boundedRPC(op string, d time.Duration, fn func() error) error {
	_, err := boundedRPCValue(op, d, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

// teardownRPCBound is the wall-clock bound for a single teardown libvirt RPC
// (destroy / shutdown / undefine / snapshot-delete / snapshot-list / lookup).
// Generous enough that a busy virtqemud finishes, small enough that a wedged one
// fails fast and loudly rather than hanging the whole command. A VAR (not a const)
// so the call-site regression test can lower it and assert the bound in milliseconds.
var teardownRPCBound = 30 * time.Second

// lookupDomain finds a domain by name (bounded: a wedged virtqemud must not hang
// destroy — the destroy path looks the domain up first; #800).
func (c *libvirtConn) lookupDomain(name string) (libvirt.Domain, error) {
	return boundedRPCValue("domain lookup", teardownRPCBound, func() (libvirt.Domain, error) {
		return rawDomainLookup(c.l, name)
	})
}

// domainState returns the current state of a domain. BOUNDED: DomainGetState is
// context-less in go-libvirt, and the destroy path reaches it repeatedly
// (gracefulStopDomain's pre-check, its SHUTOFF poll, and its post-check) BEFORE it ever
// reaches the bounded destroy — so an unbounded state read would wedge `vm destroy`
// before the fix even applies (finishing opencharly/charly#800; the same class as the
// other bounded teardown RPCs).
func (c *libvirtConn) domainState(dom libvirt.Domain) (libvirt.DomainState, error) {
	st, err := boundedRPCValue("domain state", teardownRPCBound, func() (int32, error) { return rawDomainGetState(c.l, dom) })
	if err != nil {
		return 0, err
	}
	return libvirt.DomainState(st), nil
}

// startDomain starts a defined domain. Before calling libvirt's
// DomainCreate, pre-creates any missing parent directories for
// <listen type='socket'/> graphics sockets — libvirt 12.x on Arch
// does not create `~/.config/libvirt/qemu/lib/domain-<id>-<name>/`
// in time for the QEMU bind(2) call, and QEMU fails with
// "bind: No such file or directory". Pre-creating is idempotent.
func (c *libvirtConn) startDomain(dom libvirt.Domain) error {
	if err := c.ensureDomainSocketDirs(dom); err != nil {
		return fmt.Errorf("preparing socket dirs: %w", err)
	}
	return boundedRPC("domain create", teardownRPCBound, func() error { return rawDomainCreate(c.l, dom) })
}

// Teardown RPC seams: package vars so a test can substitute a BLOCKING raw call and
// prove the CALL SITE (not merely boundedRPC in isolation) returns at the bound. Each
// defaults to the real go-libvirt method.
var (
	rawDomainLookup   = func(l *libvirt.Libvirt, name string) (libvirt.Domain, error) { return l.DomainLookupByName(name) }
	rawDomainGetState = func(l *libvirt.Libvirt, d libvirt.Domain) (int32, error) {
		st, _, err := l.DomainGetState(d, 0)
		return int32(st), err
	}
	rawDomainShutdown  = func(l *libvirt.Libvirt, d libvirt.Domain) error { return l.DomainShutdown(d) }
	rawDomainCreate    = func(l *libvirt.Libvirt, d libvirt.Domain) error { return l.DomainCreate(d) }
	rawDomainGetXML    = func(l *libvirt.Libvirt, d libvirt.Domain) (string, error) { return l.DomainGetXMLDesc(d, 0) }
	rawDomainDefine    = func(l *libvirt.Libvirt, xml string) (libvirt.Domain, error) { return l.DomainDefineXML(xml) }
	rawDomainAutostart = func(l *libvirt.Libvirt, d libvirt.Domain, flag int32) error {
		return l.DomainSetAutostart(d, flag)
	}
	rawDomainDestroy  = func(l *libvirt.Libvirt, d libvirt.Domain) error { return l.DomainDestroy(d) }
	rawDomainUndefine = func(l *libvirt.Libvirt, d libvirt.Domain) error {
		return l.DomainUndefineFlags(d, libvirt.DomainUndefineNvram|libvirt.DomainUndefineManagedSave)
	}
	rawSnapshotNum   = func(l *libvirt.Libvirt, d libvirt.Domain) (int32, error) { return l.DomainSnapshotNum(d, 0) }
	rawSnapshotNames = func(l *libvirt.Libvirt, d libvirt.Domain, n int32) ([]string, error) {
		return l.DomainSnapshotListNames(d, n, 0)
	}
	rawSnapshotLookup = func(l *libvirt.Libvirt, d libvirt.Domain, name string) (libvirt.DomainSnapshot, error) {
		return l.DomainSnapshotLookupByName(d, name, 0)
	}
	rawSnapshotDelete = func(l *libvirt.Libvirt, s libvirt.DomainSnapshot) error {
		return l.DomainSnapshotDelete(s, snapshotDeleteFlags())
	}
)

// removeDomainSnapshots deletes every snapshot record on `dom` (metadata-only; charly
// owns the disk lifecycle — charly#800). Each RPC is a bounded method, so EACH has its
// own guard and none is reachable-unbounded.
func (c *libvirtConn) removeDomainSnapshots(dom libvirt.Domain) {
	n, nerr := c.snapshotNum(dom)
	if nerr != nil || n <= 0 {
		return
	}
	names, lerr := c.snapshotNames(dom, n)
	if lerr != nil {
		return
	}
	for _, name := range names {
		snap, serr := c.snapshotLookup(dom, name)
		if serr != nil {
			continue
		}
		_ = c.snapshotDelete(snap)
	}
}

// The four snapshot-leg RPCs, each bounded and individually guarded (see the call-site
// boundedness test): a wedged virtqemud must not hang destroy at any of them (#800).
func (c *libvirtConn) snapshotNum(dom libvirt.Domain) (int32, error) {
	return boundedRPCValue("snapshot list", teardownRPCBound, func() (int32, error) { return rawSnapshotNum(c.l, dom) })
}

func (c *libvirtConn) snapshotNames(dom libvirt.Domain, n int32) ([]string, error) {
	return boundedRPCValue("snapshot names", teardownRPCBound, func() ([]string, error) { return rawSnapshotNames(c.l, dom, n) })
}

func (c *libvirtConn) snapshotLookup(dom libvirt.Domain, name string) (libvirt.DomainSnapshot, error) {
	return boundedRPCValue("snapshot lookup", teardownRPCBound, func() (libvirt.DomainSnapshot, error) {
		return rawSnapshotLookup(c.l, dom, name)
	})
}

func (c *libvirtConn) snapshotDelete(snap libvirt.DomainSnapshot) error {
	return boundedRPC("snapshot delete", teardownRPCBound, func() error { return rawSnapshotDelete(c.l, snap) })
}

// shutdownDomain requests a graceful shutdown.
func (c *libvirtConn) shutdownDomain(dom libvirt.Domain) error {
	return boundedRPC("graceful shutdown", teardownRPCBound, func() error { return rawDomainShutdown(c.l, dom) })
}

// destroyDomain forces immediate stop.
func (c *libvirtConn) destroyDomain(dom libvirt.Domain) error {
	return boundedRPC("force destroy", teardownRPCBound, func() error { return rawDomainDestroy(c.l, dom) })
}

// gracefulStopDomain requests an ACPI/agent shutdown and waits (up to the
// config StopGrace) for the domain to power off, forcing a destroy only if
// it will not stop in time. A graceful stop lets the guest flush its
// filesystems — notably the in-guest podman OVERLAY STORE: a forced
// DomainDestroy of a busy guest can leave a layer's diff dir half-written, so a
// qcow2 disk REUSED across an `charly update` recreate would then carry a torn
// image that fails `podman run` with `…/storage/overlay/<hash>: no such file`.
// No-op when the domain is already stopped or absent.
func (c *libvirtConn) gracefulStopDomain(dom libvirt.Domain) {
	state, err := c.domainState(dom)
	if err != nil || state == libvirt.DomainShutoff {
		return // unreadable (treat as absent) or already off
	}
	// Graceful ACPI poweroff is only meaningful for a RUNNING guest (it lets the
	// guest flush its filesystems, incl. the in-guest podman overlay store, before
	// power-off). For any other ACTIVE state (paused/blocked/crashed/pmsuspended)
	// there is nothing to flush — skip straight to the force below.
	if state == domainStateRunning {
		if err := c.shutdownDomain(dom); err == nil {
			// StopGate (poll.go): wait up to the config StopGrace for the domain to
			// reach SHUTOFF. Poll for SHUTOFF specifically, NOT merely "not running":
			// a transient shutdown-in-progress / paused state, or a transient
			// domainState RPC error, satisfies "not running" and would return EARLY,
			// leaving the domain still active so the caller's undefine turns it into a
			// lingering TRANSIENT running domain (the "destroy reports success but the
			// VM keeps running" bug).
			cfg := loadedReadiness().StopGate("graceful-stop domain")
			_ = pollUntil(context.Background(), cfg, func(context.Context) (bool, float64, error) {
				s, serr := c.domainState(dom)
				return serr == nil && s == libvirt.DomainShutoff, 0, nil
			})
		}
	}
	// Guarantee the domain is actually OFF before the caller undefines it: force-
	// destroy unless it reached SHUTOFF (covers ACPI-ignored/rejected, slow, wedged,
	// paused, or a guest that reboots on shutdown). DomainDestroy is the hard kill
	// proven to drive a running/active domain to SHUTOFF; a no-op error on an
	// already-off domain is harmless and ignored.
	if s, serr := c.domainState(dom); serr == nil && s != libvirt.DomainShutoff {
		_ = c.destroyDomain(dom)
	}
}

// undefineDomain removes the domain definition.
// Note: removeStorage is handled by the caller (file deletion), not via libvirt flags,
// since libvirt's storage wipe only works with managed storage pools.
//
// DomainUndefineManagedSave is required, not optional: libvirt refuses to undefine a
// domain that holds a managed save image, and the host's libvirt shutdown handler
// managed-saves every running domain across a host reboot. Without the flag, a VM that
// was running when the host rebooted becomes unremovable by every charly cleanup path.
func (c *libvirtConn) undefineDomain(dom libvirt.Domain, _ bool) error {
	return boundedRPC("undefine", teardownRPCBound, func() error { return rawDomainUndefine(c.l, dom) })
}

// activeDiskPath returns the VM's active disk path (the first
// <disk device='disk'> source file) from the domain XML. Used by the start op
// to chmod a snapshot-anchored active disk writable before qemu opens it.
func (c *libvirtConn) activeDiskPath(dom libvirt.Domain) (string, error) {
	// BOUNDED via c.getDomainXML -> rawDomainGetXML (R3: reuse, do not re-inline the
	// wrapper): the start path reads the domain XML here to chmod a snapshot-anchored
	// active disk before qemu opens it, so a wedged virtqemud must not hang it.
	xmlStr, err := c.getDomainXML(dom)
	if err != nil {
		return "", fmt.Errorf("reading domain XML: %w", err)
	}
	return firstDiskSourceFile(xmlStr)
}

// defineAndStartDomain defines a domain from XML and starts it.
// Between define and start, pre-creates any missing parent dirs for
// <listen type='socket'/> sockets (libvirt 12.x Arch bug — see
// startDomain comment).
func (c *libvirtConn) defineAndStartDomain(xmlStr, domainName string) error {
	// Reconcile a leftover domain of the SAME NAME but a drifted UUID before
	// defining. libvirt's DomainDefineXML refuses to redefine when the XML's uuid
	// differs from an existing same-name domain ("domain X already exists with uuid
	// Y"); a crashed or force-left disposable run leaves exactly such a stale domain,
	// and undefine-by-recorded-uuid then misses it. Undefine by NAME first so every
	// create — and every disposable-bed `charly update` re-run — self-heals.
	if domainName != "" {
		if existing, err := c.lookupDomain(domainName); err == nil {
			if s, serr := c.domainState(existing); serr == nil && s != libvirt.DomainShutoff {
				_ = c.destroyDomain(existing)
			}
			_ = c.undefineDomain(existing, false)
		}
	}
	dom, err := boundedRPCValue("domain define", teardownRPCBound, func() (libvirt.Domain, error) { return rawDomainDefine(c.l, xmlStr) })
	if err != nil {
		return fmt.Errorf("defining domain: %w", err)
	}
	if err := c.ensureDomainSocketDirs(dom); err != nil {
		return fmt.Errorf("preparing socket dirs: %w", err)
	}
	if err := boundedRPC("domain create", teardownRPCBound, func() error { return rawDomainCreate(c.l, dom) }); err != nil {
		return fmt.Errorf("starting domain: %w", err)
	}
	return nil
}

// ensureDomainSocketDirs reads the (possibly libvirt-populated)
// domain XML, finds every <graphics> listener with type='socket'
// and a `socket=` path, and creates the parent directory of each
// with 0700 if it doesn't exist. Idempotent.
//
// Rationale: libvirt 12.2 on Arch (and likely other rolling distros)
// does not reliably pre-create
// `~/.config/libvirt/qemu/lib/domain-<id>-<name>/` before handing
// off to QEMU, which then fails bind(2) on the SPICE socket. We
// shoulder that responsibility here.
func (c *libvirtConn) ensureDomainSocketDirs(dom libvirt.Domain) error {
	// BOUNDED via c.getDomainXML -> rawDomainGetXML: a wedged virtqemud must not hang
	// create/start at this pre-bind(2) XML read (the same class as the teardown bounds).
	xmlStr, err := c.getDomainXML(dom)
	if err != nil {
		return fmt.Errorf("reading domain XML: %w", err)
	}
	paths := extractGraphicsSocketPaths(xmlStr)
	paths = append(paths, extractChannelSocketPaths(xmlStr)...)
	for _, p := range paths {
		dir := filepath.Dir(p)
		if dir == "" || dir == "." || dir == "/" {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// extractChannelSocketPaths finds `<channel type='unix'><source path='…'/></channel>`
// paths in a libvirt domain XML. Same string-search approach as
// extractGraphicsSocketPaths — looks for any `<source>` whose
// containing element is a unix-type channel.
//
// Rationale: the qemu-guest-agent channel binds a unix socket; if
// the parent directory doesn't exist (common when authors compose
// the path with templating like {{.VmStateDir}}/qga.sock and the
// VM state dir was just created), QEMU's bind(2) fails. Mirroring
// the existing graphics-socket pre-create logic.
func extractChannelSocketPaths(xmlStr string) []string {
	var out []string
	remaining := xmlStr
	for {
		i := strings.Index(remaining, "<channel")
		if i < 0 {
			return out
		}
		// Slice the channel element body to its closing tag.
		end := strings.Index(remaining[i:], "</channel>")
		if end < 0 {
			return out
		}
		body := remaining[i : i+end]
		remaining = remaining[i+end:]
		if !strings.Contains(body, `type='unix'`) && !strings.Contains(body, `type="unix"`) {
			continue
		}
		// Look for <source path='…'/> (or path="…").
		for _, q := range []string{"path='", `path="`} {
			_, after, ok := strings.Cut(body, q)
			if !ok {
				continue
			}
			rest := after
			ei := strings.IndexAny(rest, `'"`)
			if ei < 0 {
				continue
			}
			out = append(out, rest[:ei])
			break
		}
	}
}

// extractGraphicsSocketPaths finds `<listen type='socket' socket='…'/>`
// paths in a libvirt domain XML. String-search rather than a full XML
// parse — keeps the dependency surface small and doesn't crash on
// any edge shapes libvirt might emit.
func extractGraphicsSocketPaths(xmlStr string) []string {
	var out []string
	remaining := xmlStr
	for {
		i := strings.Index(remaining, "<listen")
		if i < 0 {
			return out
		}
		end := strings.Index(remaining[i:], "/>")
		if end < 0 {
			end = strings.Index(remaining[i:], ">")
			if end < 0 {
				return out
			}
		}
		tag := remaining[i : i+end]
		remaining = remaining[i+end:]
		if !strings.Contains(tag, `type='socket'`) && !strings.Contains(tag, `type="socket"`) {
			continue
		}
		// Look for socket='…' or socket="…"
		for _, q := range []string{"socket='", `socket="`} {
			_, after, ok := strings.Cut(tag, q)
			if !ok {
				continue
			}
			rest := after
			ei := strings.IndexAny(rest, `'"`)
			if ei < 0 {
				continue
			}
			out = append(out, rest[:ei])
			break
		}
	}
}

// getDomainXML returns the XML description of a domain.
func (c *libvirtConn) getDomainXML(dom libvirt.Domain) (string, error) {
	return boundedRPCValue("domain XML", teardownRPCBound, func() (string, error) { return rawDomainGetXML(c.l, dom) })
}

// redefineDomain redefines a domain from XML string.
func (c *libvirtConn) redefineDomain(xmlStr string) error {
	_, err := boundedRPCValue("domain define", teardownRPCBound, func() (libvirt.Domain, error) { return rawDomainDefine(c.l, xmlStr) })
	return err
}

// setDomainAutostart toggles libvirt's per-domain autostart flag. The
// flag is a libvirt domain property (not part of the domain XML), so it
// survives DomainDefineXML re-definitions; we re-assert it on create
// anyway. For qemu:///session the flag only triggers at host boot when
// the user session lingers — see ensureBootAutostartPrereqs.
func (c *libvirtConn) setDomainAutostart(name string, on bool) error {
	dom, err := c.lookupDomain(name)
	if err != nil {
		return fmt.Errorf("looking up domain %s: %w", name, err)
	}
	flag := int32(0)
	if on {
		flag = 1
	}
	if err := boundedRPC("domain autostart", teardownRPCBound, func() error { return rawDomainAutostart(c.l, dom, flag) }); err != nil {
		return fmt.Errorf("setting autostart on %s: %w", name, err)
	}
	return nil
}

// listCharlyDomains returns all domains with the "charly-" prefix.
func (c *libvirtConn) listCharlyDomains() ([]domainInfo, error) {
	flags := libvirt.ConnectListDomainsActive | libvirt.ConnectListDomainsInactive
	domains, _, err := c.l.ConnectListAllDomains(1, flags)
	if err != nil {
		return nil, err
	}

	var results []domainInfo
	for _, dom := range domains {
		name := dom.Name
		if !strings.HasPrefix(name, "charly-") {
			continue
		}
		state, stateErr := c.domainState(dom)
		stateStr := "unknown"
		if stateErr == nil {
			stateStr = domainStateString(state)
		}
		results = append(results, domainInfo{Name: name, State: stateStr})
	}
	return results, nil
}

type domainInfo struct {
	Name  string
	State string
}

func domainStateString(state libvirt.DomainState) string {
	switch state {
	case libvirt.DomainRunning:
		return "running"
	case libvirt.DomainShutoff:
		return "shut off"
	case libvirt.DomainPaused:
		return "paused"
	case libvirt.DomainShutdown:
		return "shutting down"
	case libvirt.DomainCrashed:
		return "crashed"
	case libvirt.DomainPmsuspended:
		return "suspended"
	default:
		return "unknown"
	}
}

// remoteVirtqemudSocketPath discovers the remote user's session
// virtqemud socket path via the SSH connection. Probes (in order):
//  1. $XDG_RUNTIME_DIR/libvirt/virtqemud-sock (modular libvirt ≥ 8)
//  2. /run/user/$(id -u)/libvirt/virtqemud-sock
//  3. $XDG_RUNTIME_DIR/libvirt/libvirt-sock (legacy monolithic)
//
// Returns the first path that exists on the remote host.
func remoteVirtqemudSocketPath(client *ssh.Client) (string, error) {
	// Single command that prints the first existing candidate. Cheaper
	// than three separate round-trips.
	script := `
set -e
for p in "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/libvirt/virtqemud-sock" \
         "/run/user/$(id -u)/libvirt/virtqemud-sock" \
         "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/libvirt/libvirt-sock"; do
  if [ -S "$p" ]; then
    printf "%s" "$p"
    exit 0
  fi
done
echo "no libvirt session socket found" >&2
exit 1
`
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close() //nolint:errcheck
	out, err := session.Output(script)
	if err != nil {
		return "", fmt.Errorf("probing remote socket path: %w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("remote returned empty socket path")
	}
	return path, nil
}
