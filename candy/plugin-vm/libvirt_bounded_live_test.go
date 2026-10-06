package vm

import (
	"io"
	"strings"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
)

// TestLibvirtBounded_AgainstLiveDomain is the LIVE leg for plugin-vm#73: the bounded
// introspection methods that are safe to drive against a REAL running charly domain
// are exercised here — listAllDomains, domainInfo, domainXML (read-only), plus
// listSnapshots, screenshot, and agentCommand (which may legitimately error on a
// headless/agent-less guest; the point is that each RETURNS bounded). It proves those
// wrappers do not break the real libvirt calls they enclose. The other EIGHT bounded
// methods are NOT driven here — the live test is deliberately non-mutating so it is
// safe against any running VM: sendKey, updateDeviceFlags, qmpCommand, and the five
// snapshot RPCs snapshotCreateXML, snapshotLookupByName, snapshotXMLDesc,
// revertToSnapshot, snapshotDeleteByHandle (three of which mutate — create/revert/
// delete; the other two are read-only lookups). Their bound is proven by the
// blocking-seam guards in libvirt_bounded_test.go. Skipped without a session / a
// running charly-* domain.
func TestLibvirtBounded_AgainstLiveDomain(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a live libvirt session + a running charly-* domain")
	}
	conn, err := connectLibvirt("")
	if err != nil {
		t.Skipf("no libvirt session: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	// Bounded list (real RPC) — find a running charly-* domain to exercise the rest.
	doms, err := conn.listAllDomains(1, libvirt.ConnectListDomainsActive)
	if err != nil {
		t.Fatalf("bounded listAllDomains against live libvirt: %v", err)
	}
	var dom libvirt.Domain
	found := false
	for _, d := range doms {
		if strings.HasPrefix(d.Name, "charly-") {
			dom, found = d, true
			break
		}
	}
	if !found {
		t.Skip("no running charly-* domain to exercise the verb against")
	}

	// Bounded introspection against the REAL domain.
	st, maxMem, mem, ncpu, cputime, err := conn.domainInfo(dom)
	if err != nil {
		t.Fatalf("bounded domainInfo against live domain %q: %v", dom.Name, err)
	}
	xml, err := conn.domainXML(dom, 0)
	if err != nil {
		t.Fatalf("bounded domainXML against live domain %q: %v", dom.Name, err)
	}
	t.Logf("LIVE domain=%s state=%d maxMem=%d mem=%d ncpu=%d cputime=%d xmlBytes=%d",
		dom.Name, st, maxMem, mem, ncpu, cputime, len(xml))
	if len(xml) == 0 || !strings.Contains(xml, dom.Name) {
		t.Fatalf("bounded domainXML returned %d bytes not naming %q", len(xml), dom.Name)
	}

	// These may fail legitimately on a headless / snapshot-less / agent-less guest;
	// what is under test is that each RETURNS (bounded), not that it succeeds.
	if _, err := conn.listSnapshots(dom); err != nil {
		t.Logf("LIVE listSnapshots returned (bounded) err: %v", err)
	}
	if _, err := conn.screenshot(dom, io.Discard, 0, 0); err != nil {
		t.Logf("LIVE screenshot returned (bounded) err: %v", err)
	}
	if _, err := agentCommand(conn.l, dom, `{"execute":"guest-ping"}`, 5, 0); err != nil {
		t.Logf("LIVE agentCommand returned (bounded) err: %v", err)
	}
}
