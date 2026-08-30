package vm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The QMP client is exercised against a REAL qemu process, not a fake socket.
//
// A hand-written protocol client is exactly the kind of code that passes against a mock
// written from the same misreading that produced the client. The two things most likely to
// be wrong — the greeting/capabilities handshake, and async `event` frames arriving between
// a command and its reply — are properties of qemu's actual behaviour, so qemu is what
// answers here.
//
// The guest is deliberately nothing: no disk, no kernel. It reaches the firmware's "no
// bootable device" screen, which is a screen, which is all these verbs need.
func startTestQemu(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		t.Skip("qemu-system-x86_64 not installed")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "qmp.sock")
	cmd := exec.Command(bin,
		"-nodefaults",
		"-machine", "pc,accel=tcg",
		"-m", "128",
		"-display", "none",
		"-vga", "std",
		"-qmp", "unix:"+sock+",server,nowait",
	)
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start qemu: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	// Wait for the socket rather than sleeping a fixed amount: a sleep is either flaky or
	// slow, and both are worse than a bounded poll (R4).
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return sock
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("qemu never created its QMP socket at %s", sock)
	return ""
}

func TestQMP_HandshakeAndHumanMonitor(t *testing.T) {
	sock := startTestQemu(t)
	q, err := dialQMP(sock, 15*time.Second)
	if err != nil {
		t.Fatalf("dialQMP against a real qemu: %v", err)
	}
	defer q.Close() //nolint:errcheck

	// `info status` is the cheapest command that proves the whole path: greeting parsed,
	// capabilities negotiated, command written, reply read back as text.
	out, err := q.humanMonitor("info status")
	if err != nil {
		t.Fatalf("human-monitor-command: %v", err)
	}
	if !strings.Contains(out, "VM status") {
		t.Fatalf("unexpected `info status` output from a real qemu: %q", out)
	}
}

// The screendump path end to end: the monitor writes a capture, and the verb's own
// normalizer turns whatever format qemu chose into a PNG.
func TestQMP_ScreendumpConvertsToPNG(t *testing.T) {
	sock := startTestQemu(t)
	q, err := dialQMP(sock, 15*time.Second)
	if err != nil {
		t.Fatalf("dialQMP: %v", err)
	}
	defer q.Close() //nolint:errcheck

	dir := t.TempDir()
	raw := filepath.Join(dir, "screen.ppm")
	// The PRODUCTION helper, not a re-implementation of it in the test.
	if err := screendumpOverQMP(q, "test-domain", raw); err != nil {
		t.Fatalf("screendumpOverQMP against a real qemu: %v", err)
	}
	st, err := os.Stat(raw)
	if err != nil {
		t.Fatalf("qemu did not write the capture: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("qemu wrote an EMPTY capture")
	}
	head, _ := os.ReadFile(raw)
	t.Logf("EVIDENCE: qemu screendump wrote %d bytes starting %q", st.Size(), head[:min(16, len(head))])

	png := filepath.Join(dir, "screen.png")
	if err := capturedScreenToPNG(raw, png); err != nil {
		t.Fatalf("converting a REAL qemu capture: %v", err)
	}
	body, err := os.ReadFile(png)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 8 || string(body[:4]) != "\x89PNG" {
		t.Fatalf("the normalizer did not produce a PNG from a real qemu capture (got %q)", body[:min(8, len(body))])
	}
}

// sendkey over the real monitor, including the failure direction: qemu reports an unknown
// key in the reply TEXT with a zero QMP status, so a client that discards that text reports
// success for keys the guest never received.
func TestQMP_SendkeyReportsAnUnknownKey(t *testing.T) {
	sock := startTestQemu(t)
	q, err := dialQMP(sock, 15*time.Second)
	if err != nil {
		t.Fatalf("dialQMP: %v", err)
	}
	defer q.Close() //nolint:errcheck

	// Every key this package's vocabulary can produce must be accepted by the real monitor.
	// This is the qemu-side half of TestEveryTypeableKeyTranslatesToVirsh: without it the
	// vocabulary can drift from qemu just as easily as from virsh.
	var printable strings.Builder
	for r := rune(32); r < 127; r++ {
		printable.WriteRune(r)
	}
	keys, err := TextToGuestKeys(printable.String() + "\n\t")
	if err != nil {
		t.Fatal(err)
	}
	// The PRODUCTION burst helper, so this is the code `vm type` actually runs.
	if err := sendKeysOverQMP(q, "test-domain", keys); err != nil {
		t.Fatalf("the real qemu monitor rejected a key `vm type` can emit: %v", err)
	}

	// The failure direction, measured from qemu rather than assumed. qemu answers an unknown
	// key in the reply TEXT with a ZERO QMP status, so a client that discards that text
	// reports success for keys the guest never received — and the operator then concludes the
	// GUEST is unresponsive. This is the control for that.
	err = sendKeysOverQMP(q, "test-domain", []string{"nosuchkey"})
	if err == nil {
		t.Fatal("an unknown key must be an error: qemu signals it in the reply text, not the status")
	}
	if !strings.Contains(err.Error(), "nosuchkey") {
		t.Errorf("the error must name the key that was refused, got: %v", err)
	}
	t.Logf("EVIDENCE: qemu on an unknown key: %v", err)
}
