package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A headless guest was undiagnosable: `vm screenshot` refuses (no video device) and
// `vm console` refuses (no controlling TTY), and a pty keeps no history so a guest that
// already failed has nothing left to show. Every bootstrap VM is headless by design
// (console=ttyS0, no graphics), which is exactly the class whose boot most needs reading.
func TestRenderDomainXML_SynthesizedSerialCarriesALog(t *testing.T) {
	t.Setenv("CHARLY_VM_STATE_DIR", t.TempDir())

	xmlStr, err := RenderDomainXML(&VmSpec{Firmware: "bios", Machine: "q35"}, VmRuntimeParams{
		Name:     "charly-headless-vm",
		RamMB:    2048,
		Cpus:     2,
		HostArch: "x86_64",
	})
	if err != nil {
		t.Fatalf("RenderDomainXML: %v", err)
	}
	if !strings.Contains(xmlStr, "<serial") {
		t.Fatalf("no serial device synthesized at all:\n%s", xmlStr)
	}
	// The log must name THIS domain: two beds sharing one kind:vm entity get distinct
	// domains, and a shared log would interleave two guests' consoles into one file.
	if !strings.Contains(xmlStr, filepath.Join("charly-headless-vm", "serial.log")) {
		t.Errorf("the synthesized serial carries no per-domain <log> element, so a headless "+
			"guest's console is unreadable — `vm console` needs a TTY and a pty keeps no "+
			"history. got:\n%s", xmlStr)
	}
}

// NEGATIVE CONTROL for the derivation, not just its presence. An empty domain name must
// yield no log element rather than a path rooted at the state dir itself — which would make
// every unnamed domain write into one shared file.
func TestSerialLogPathFor_RefusesAnEmptyDomain(t *testing.T) {
	t.Setenv("CHARLY_VM_STATE_DIR", t.TempDir())
	if got := serialLogPathFor(""); got != "" {
		t.Errorf("an empty domain name must yield no log path, got %q", got)
	}
}

func TestSerialLogPathFor_IsPerDomain(t *testing.T) {
	t.Setenv("CHARLY_VM_STATE_DIR", t.TempDir())
	a, b := serialLogPathFor("charly-bed-a"), serialLogPathFor("charly-bed-b")
	if a == "" || b == "" {
		t.Fatalf("expected paths, got %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("two domains share one serial log (%q) — their consoles would interleave", a)
	}
}

// The absent-file message is the whole usability of this verb. A domain defined before
// serial logging carries no <log> element and libvirt will NEVER write one for it, so a bare
// "no such file" sends the reader looking for a missing directory instead of recreating.
func TestDumpVmSerialLog_AbsentLogNamesTheFix(t *testing.T) {
	t.Setenv("CHARLY_VM_STATE_DIR", t.TempDir())
	err := dumpVmSerialLog("charly-never-created", 0)
	if err == nil {
		t.Fatal("expected an error for a domain with no serial log")
	}
	for _, want := range []string{"Recreate the domain", "charly vm create"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the fix (%q); got: %v", want, err)
		}
	}
}

func TestDumpVmSerialLog_TailsWithLines(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", root)
	dir := filepath.Join(root, "charly-tail-vm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "serial.log"),
		[]byte("one\ntwo\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A boot log is long and the interesting part is the end; reading it all into an agent's
	// context is the failure this flag avoids.
	out := captureStdout(t, func() {
		if err := dumpVmSerialLog("charly-tail-vm", 2); err != nil {
			t.Fatalf("dump: %v", err)
		}
	})
	if out != "four\nfive\n" {
		t.Errorf("--lines 2 must print the LAST two lines, got %q", out)
	}
	all := captureStdout(t, func() {
		if err := dumpVmSerialLog("charly-tail-vm", 0); err != nil {
			t.Fatalf("dump: %v", err)
		}
	})
	if all != "one\ntwo\nthree\nfour\nfive\n" {
		t.Errorf("--lines 0 must print everything verbatim, got %q", all)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			sb.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}
