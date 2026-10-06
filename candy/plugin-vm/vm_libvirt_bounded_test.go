package vm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/go-libvirt"
)

// TestBoundedRPC_ReturnsOnTimeout is the regression guard for opencharly/charly#800:
// `charly vm destroy` hung indefinitely because the libvirt teardown RPCs
// (DomainDestroy/DomainShutdown/DomainUndefineFlags/DomainSnapshotDelete) are
// context-less in go-libvirt, so a wedged virtqemud blocked them forever. boundedRPC
// must return a NAMED error at the bound WITHOUT waiting for the underlying call.
func TestBoundedRPC_ReturnsOnTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // release the abandoned goroutine so the test does not leak it
	start := time.Now()
	err := boundedRPC("force destroy", 50*time.Millisecond, func() error {
		<-block // never returns on its own — the wedged-virtqemud shape
		return nil
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a wedged libvirt RPC must return a bounded, named error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("boundedRPC must return AT the bound, not block: took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "force destroy") || !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("the error must NAME the op and the bounded failure, got: %v", err)
	}
}

// TestBoundedRPC_PassesThroughSuccess proves the bound does not alter the fast path:
// a call that returns promptly yields ITS OWN error unchanged (identity, not merely
// non-nil).
func TestBoundedRPC_PassesThroughSuccess(t *testing.T) {
	want := errors.New("sentinel")
	got := boundedRPC("undefine", time.Second, func() error { return want })
	if got != want {
		t.Fatalf("boundedRPC must propagate the fast call's OWN error unchanged: got %v, want %v", got, want)
	}
}

// blockingSeams swaps every teardown raw seam for a call that blocks forever — the
// wedged-virtqemud shape — and restores them on cleanup.
func blockingSeams(t *testing.T) {
	t.Helper()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })

	od, os_, ou := rawDomainDestroy, rawDomainShutdown, rawDomainUndefine
	ol, og, on, onm, olk, odel := rawDomainLookup, rawDomainGetState, rawSnapshotNum, rawSnapshotNames, rawSnapshotLookup, rawSnapshotDelete
	oc, ode, ox, oa := rawDomainCreate, rawDomainDefine, rawDomainGetXML, rawDomainAutostart
	t.Cleanup(func() {
		rawDomainDestroy, rawDomainShutdown, rawDomainUndefine = od, os_, ou
		rawDomainLookup, rawDomainGetState, rawSnapshotNum, rawSnapshotNames, rawSnapshotLookup, rawSnapshotDelete = ol, og, on, onm, olk, odel
		rawDomainCreate, rawDomainDefine, rawDomainGetXML, rawDomainAutostart = oc, ode, ox, oa
	})
	rawDomainDestroy = func(*libvirt.Libvirt, libvirt.Domain) error { <-block; return nil }
	rawDomainShutdown = func(*libvirt.Libvirt, libvirt.Domain) error { <-block; return nil }
	rawDomainUndefine = func(*libvirt.Libvirt, libvirt.Domain) error { <-block; return nil }
	rawDomainLookup = func(*libvirt.Libvirt, string) (libvirt.Domain, error) { <-block; return libvirt.Domain{}, nil }
	rawDomainGetState = func(*libvirt.Libvirt, libvirt.Domain) (int32, error) { <-block; return 0, nil }
	rawSnapshotNum = func(*libvirt.Libvirt, libvirt.Domain) (int32, error) { <-block; return 0, nil }
	rawSnapshotNames = func(*libvirt.Libvirt, libvirt.Domain, int32) ([]string, error) { <-block; return nil, nil }
	rawSnapshotLookup = func(*libvirt.Libvirt, libvirt.Domain, string) (libvirt.DomainSnapshot, error) {
		<-block
		return libvirt.DomainSnapshot{}, nil
	}
	rawSnapshotDelete = func(*libvirt.Libvirt, libvirt.DomainSnapshot) error { <-block; return nil }
	rawDomainCreate = func(*libvirt.Libvirt, libvirt.Domain) error { <-block; return nil }
	rawDomainDefine = func(*libvirt.Libvirt, string) (libvirt.Domain, error) { <-block; return libvirt.Domain{}, nil }
	rawDomainGetXML = func(*libvirt.Libvirt, libvirt.Domain) (string, error) { <-block; return "", nil }
	rawDomainAutostart = func(*libvirt.Libvirt, libvirt.Domain, int32) error { <-block; return nil }
}

// TestTeardownCallSitesAreBounded drives the REAL teardown methods (not boundedRPC in
// isolation) with every raw libvirt seam blocking forever. Each must return at the bound
// with a NAMED error. If a call site ever drops its boundedRPC wrapper and calls its seam
// directly, the corresponding subtest hangs (the bound is what turns the wedge into a
// return), so this fails exactly when the wrapper is missing at the call site.
func TestTeardownCallSitesAreBounded(t *testing.T) {
	oldBound := teardownRPCBound
	teardownRPCBound = 150 * time.Millisecond
	t.Cleanup(func() { teardownRPCBound = oldBound })

	conn := &libvirtConn{} // seams ignore l; the methods must not need a live connection
	dom := libvirt.Domain{}
	bound := 3 * time.Second

	cases := []struct {
		label  string
		prereq func() // releases a sibling seam the method calls BEFORE the one under test
		run    func() error
		namedE bool // true when the call site surfaces a NAMED bounded error (leg swallows → false)
	}{
		{"force destroy", nil, func() error { return conn.destroyDomain(dom) }, true},
		{"graceful shutdown", nil, func() error { return conn.shutdownDomain(dom) }, true},
		{"undefine", nil, func() error { return conn.undefineDomain(dom, false) }, true},
		{"domain lookup", nil, func() error { _, err := conn.lookupDomain("x"); return err }, true},
		{"domain state", nil, func() error { _, err := conn.domainState(dom); return err }, true},
		// Each snapshot-leg RPC is its own bounded method -> its own guard. Driving each
		// directly proves the wrapper at EACH (the old leg-level test short-circuited on the
		// first timeout and never reached the inner RPCs).
		{"snapshot list", nil, func() error { _, err := conn.snapshotNum(dom); return err }, true},
		{"snapshot names", nil, func() error { _, err := conn.snapshotNames(dom, 3); return err }, true},
		{"snapshot lookup", nil, func() error { _, err := conn.snapshotLookup(dom, "s"); return err }, true},
		{"snapshot delete", nil, func() error { return conn.snapshotDelete(libvirt.DomainSnapshot{}) }, true},
		// The create/define/XML/autostart group (R2 completion, c7c8119). Their methods
		// pre-call a SIBLING seam — startDomain reads the domain XML for socket dirs, and
		// setDomainAutostart looks the domain up first — so prereq releases THAT seam so
		// the call site UNDER TEST is actually reached (otherwise the guard would prove
		// the sibling's wrapper, not this one's).
		{"domain XML", nil, func() error { _, err := conn.getDomainXML(dom); return err }, true},
		{"domain define", nil, func() error { return conn.redefineDomain("<domain/>") }, true},
		// ensureDomainSocketDirs is the create/start pre-bind(2) XML read, bounded via
		// c.getDomainXML; assert only that it RETURNS at the bound (its error is wrapped
		// as "reading domain XML: …", so the name is the inner op, not this call site).
		{"socket dirs", nil, func() error { return conn.ensureDomainSocketDirs(dom) }, false},
		{"domain create", func() {
			rawDomainGetXML = func(*libvirt.Libvirt, libvirt.Domain) (string, error) { return "<domain/>", nil }
		}, func() error { return conn.startDomain(dom) }, true},
		{"domain autostart", func() {
			rawDomainLookup = func(*libvirt.Libvirt, string) (libvirt.Domain, error) { return libvirt.Domain{}, nil }
		}, func() error { return conn.setDomainAutostart("x", true) }, true},
		// The defineAndStartDomain RECONCILE call site itself (finding 1): release every
		// sibling seam so ONLY the leftover lookup can block, then drive the method. If
		// that call site ever drops `c.lookupDomain` for a bare `rawDomainLookup`, the
		// wedge is reached unbounded and this hangs → fails at the bound. A clean return
		// (the lookup's bounded error is swallowed by the `err == nil` guard, then define
		// + create succeed) is the pass.
		{"define and start reconcile", func() {
			rawDomainDefine = func(*libvirt.Libvirt, string) (libvirt.Domain, error) { return libvirt.Domain{}, nil }
			rawDomainGetXML = func(*libvirt.Libvirt, libvirt.Domain) (string, error) { return "<domain/>", nil }
			rawDomainCreate = func(*libvirt.Libvirt, libvirt.Domain) error { return nil }
		}, func() error { return conn.defineAndStartDomain("<domain/>", "x") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			// Isolate per subtest: a prereq override must not leak into a later case.
			blockingSeams(t)
			if tc.prereq != nil {
				tc.prereq()
			}
			done := make(chan error, 1)
			go func() { done <- tc.run() }()
			select {
			case err := <-done:
				if tc.namedE && (err == nil || !strings.Contains(err.Error(), tc.label)) {
					t.Fatalf("%s call site must return a NAMED bounded error, got %v", tc.label, err)
				}
			case <-time.After(bound):
				t.Fatalf("%s call site did NOT return at the bound — the boundedRPC wrapper is missing at the call site", tc.label)
			}
		})
	}
}
