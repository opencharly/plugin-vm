package vm

import (
	"errors"
	"image"
	"io"
	"strings"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// libvirt_bounded_test.go — call-site guards for the `verb:libvirt` bounds
// (plugin-vm#73). The pattern is the one proven in vm_libvirt_bounded_test.go
// (#74): substitute a BLOCKING raw seam, drive the REAL method, and assert it
// returns at the (lowered) bound with a named error. Deleting a method's
// boundedRPC/boundedRPCValue wrapper hangs its subtest, so this fails exactly
// when a wrapper is missing at the call site.

// blockingVerbSeams swaps every verb-path raw seam for a call that blocks forever.
// The block channel is NEVER closed: a bounded call site returns at its bound and
// abandons its goroutine blocked on `block`, and that goroutine must stay blocked
// for the whole process — restoring the real seam under a released goroutine would
// run the real body against this test's nil libvirt handle and panic (the defect
// fixed in #74's blockingSeams).
func blockingVerbSeams(t *testing.T) {
	t.Helper()
	block := make(chan struct{})

	oList, oInfo, oXML := rawConnectListAllDomains, rawDomainGetInfo, rawDomainGetXMLFlags
	oSend, oUpd, oQMP := rawDomainSendKey, rawDomainUpdateDeviceFlags, rawQEMUDomainMonitorCommand
	oLSS, oSCreate, oSLookup := rawDomainListAllSnapshots, rawDomainSnapshotCreateXML, rawDomainSnapshotLookupByName
	oSXML, oRevert, oSDelete := rawDomainSnapshotGetXMLDesc, rawDomainRevertToSnapshot, rawDomainSnapshotDelete
	oAgent, oShot := rawQEMUDomainAgentCommand, rawDomainScreenshot
	t.Cleanup(func() {
		rawConnectListAllDomains, rawDomainGetInfo, rawDomainGetXMLFlags = oList, oInfo, oXML
		rawDomainSendKey, rawDomainUpdateDeviceFlags, rawQEMUDomainMonitorCommand = oSend, oUpd, oQMP
		rawDomainListAllSnapshots, rawDomainSnapshotCreateXML, rawDomainSnapshotLookupByName = oLSS, oSCreate, oSLookup
		rawDomainSnapshotGetXMLDesc, rawDomainRevertToSnapshot, rawDomainSnapshotDelete = oSXML, oRevert, oSDelete
		rawQEMUDomainAgentCommand, rawDomainScreenshot = oAgent, oShot
	})

	rawConnectListAllDomains = func(*libvirt.Libvirt, int32, libvirt.ConnectListAllDomainsFlags) ([]libvirt.Domain, uint32, error) {
		<-block
		return nil, 0, nil
	}
	rawDomainGetInfo = func(*libvirt.Libvirt, libvirt.Domain) (uint8, uint64, uint64, uint16, uint64, error) {
		<-block
		return 0, 0, 0, 0, 0, nil
	}
	rawDomainGetXMLFlags = func(*libvirt.Libvirt, libvirt.Domain, libvirt.DomainXMLFlags) (string, error) {
		<-block
		return "", nil
	}
	rawDomainSendKey = func(*libvirt.Libvirt, libvirt.Domain, uint32, uint32, []uint32, uint32) error {
		<-block
		return nil
	}
	rawDomainUpdateDeviceFlags = func(*libvirt.Libvirt, libvirt.Domain, string, libvirt.DomainDeviceModifyFlags) error {
		<-block
		return nil
	}
	rawQEMUDomainMonitorCommand = func(*libvirt.Libvirt, libvirt.Domain, string, uint32) (string, error) {
		<-block
		return "", nil
	}
	rawDomainListAllSnapshots = func(*libvirt.Libvirt, libvirt.Domain, int32, uint32) ([]libvirt.DomainSnapshot, int32, error) {
		<-block
		return nil, 0, nil
	}
	rawDomainSnapshotCreateXML = func(*libvirt.Libvirt, libvirt.Domain, string, uint32) (libvirt.DomainSnapshot, error) {
		<-block
		return libvirt.DomainSnapshot{}, nil
	}
	rawDomainSnapshotLookupByName = func(*libvirt.Libvirt, libvirt.Domain, string, uint32) (libvirt.DomainSnapshot, error) {
		<-block
		return libvirt.DomainSnapshot{}, nil
	}
	rawDomainSnapshotGetXMLDesc = func(*libvirt.Libvirt, libvirt.DomainSnapshot, uint32) (string, error) {
		<-block
		return "", nil
	}
	rawDomainRevertToSnapshot = func(*libvirt.Libvirt, libvirt.DomainSnapshot, uint32) error { <-block; return nil }
	rawDomainSnapshotDelete = func(*libvirt.Libvirt, libvirt.DomainSnapshot, libvirt.DomainSnapshotDeleteFlags) error {
		<-block
		return nil
	}
	rawQEMUDomainAgentCommand = func(*libvirt.Libvirt, libvirt.Domain, string, int32, uint32) (libvirt.OptString, error) {
		<-block
		return libvirt.OptString{}, nil
	}
	rawDomainScreenshot = func(*libvirt.Libvirt, libvirt.Domain, io.Writer, uint32, uint32) (libvirt.OptString, error) {
		<-block
		return libvirt.OptString{}, nil
	}
}

// TestLibvirtVerbCallSitesAreBounded drives each bounded verb method with every raw
// seam blocking forever; each must return at the bound with a NAMED error naming its
// op (or, for the screenshot stream, at screenshotBound).
func TestLibvirtVerbCallSitesAreBounded(t *testing.T) {
	oldProbe, oldShot := libvirtProbeBound, screenshotBound
	libvirtProbeBound = 150 * time.Millisecond
	screenshotBound = 150 * time.Millisecond
	t.Cleanup(func() { libvirtProbeBound, screenshotBound = oldProbe, oldShot })

	conn := &libvirtConn{} // seams ignore l
	dom := libvirt.Domain{}
	bound := 3 * time.Second

	cases := []struct {
		label string
		run   func() error
		want  string
	}{
		{"list domains", func() error { _, err := conn.listAllDomains(1, 0); return err }, "list domains"},
		{"domain info", func() error { _, _, _, _, _, err := conn.domainInfo(dom); return err }, "domain info"},
		{"domain xml", func() error { _, err := conn.domainXML(dom, 0); return err }, "domain xml"},
		{"send key", func() error { return conn.sendKey(dom, 1, 0, nil, 0) }, "send key"},
		{"update device flags", func() error { return conn.updateDeviceFlags(dom, "<x/>", 0) }, "update device flags"},
		{"qmp command", func() error { _, err := conn.qmpCommand(dom, "{}", 0); return err }, "qmp command"},
		{"snapshot list", func() error { _, err := conn.listSnapshots(dom); return err }, "snapshot list"},
		{"snapshot create", func() error { _, err := conn.snapshotCreateXML(dom, "<x/>", 0); return err }, "snapshot create"},
		{"snapshot lookup", func() error { _, err := conn.snapshotLookupByName(dom, "s"); return err }, "snapshot lookup"},
		{"snapshot xml", func() error { _, err := conn.snapshotXMLDesc(libvirt.DomainSnapshot{}); return err }, "snapshot xml"},
		{"snapshot revert", func() error { return conn.revertToSnapshot(libvirt.DomainSnapshot{}) }, "snapshot revert"},
		{"snapshot delete", func() error { return conn.snapshotDeleteByHandle(libvirt.DomainSnapshot{}) }, "snapshot delete"},
		{"guest agent", func() error { _, err := agentCommand(nil, dom, "{}", 0, 0); return err }, "guest agent command"},
		{"screenshot", func() error { _, err := conn.screenshot(dom, io.Discard, 0, 0); return err }, "domain screenshot"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			blockingVerbSeams(t)
			done := make(chan error, 1)
			go func() { done <- tc.run() }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("%s call site must return a named bounded error containing %q, got %v", tc.label, tc.want, err)
				}
			case <-time.After(bound):
				t.Fatalf("%s call site did NOT return at the bound — the boundedRPC wrapper is missing at the call site", tc.label)
			}
		})
	}
}

// errFrameSource is a frameSource stub whose Screenshot always errors (the
// wedged-stream shape); okFrameSource always returns a valid 1x1 image (the healthy
// shape, used to exercise the writer path).
type errFrameSource struct{ err error }

func (s errFrameSource) Screenshot() (image.Image, error) { return nil, s.err }

type okFrameSource struct{}

func (okFrameSource) Screenshot() (image.Image, error) {
	return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
}

// TestRecorderStopsOnConsecutiveScreenshotFailures proves the bounded-screenshot
// stop: after recorderConsecutiveFailLimit consecutive Screenshot errors the recorder
// returns the error instead of polling forever (each further poll would abandon
// another blocked goroutine on the same wedged stream — plugin-vm#73).
func TestRecorderStopsOnConsecutiveScreenshotFailures(t *testing.T) {
	old := recorderConsecutiveFailLimit
	recorderConsecutiveFailLimit = 3
	t.Cleanup(func() { recorderConsecutiveFailLimit = old })

	want := errors.New("domain screenshot: libvirt did not respond")
	done := make(chan struct{}) // never closed: the stop must come from the fail limit
	_, err := writeFrames(errFrameSource{want}, 5*time.Millisecond, io.Discard, done)
	if err == nil || !errors.Is(err, want) {
		t.Fatalf("writeFrames must return the screenshot error after %d consecutive failures, got %v", recorderConsecutiveFailLimit, err)
	}
}

// TestRecorderSurfacesWriterFailure proves a mid-recording output failure is a real
// failure (wrapped as errRecorderWriterFailed), not a clean stop.
func TestRecorderSurfacesWriterFailure(t *testing.T) {
	done := make(chan struct{})
	_, err := writeFrames(okFrameSource{}, 5*time.Millisecond, failWriter{}, done)
	if !errors.Is(err, errRecorderWriterFailed) {
		t.Fatalf("a writer failure must be wrapped as errRecorderWriterFailed, got %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
