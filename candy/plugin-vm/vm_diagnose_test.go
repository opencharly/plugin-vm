package vm

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencharly/sdk/vmshared"
)

// ─── seed ──────────────────────────────────────────────────────────────────────────────

// The seed reader is verified against charly's OWN writer, not against a hand-built
// fixture. That is the point: `charly vm seed cat` exists to answer "what did charly
// actually put on the answers volume", and a test that read a fixture some test author
// wrote could pass while the real writer and the real reader disagreed — which is exactly
// the failure that sent this investigation to `isoinfo` in the first place.
func writeTestSeed(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("xorriso"); err != nil {
		t.Skip("xorriso not installed — the sdk's ISO writer needs it")
	}
	path := filepath.Join(t.TempDir(), "seed.iso")
	if err := vmshared.WriteLabeledISO(path, "cidata", files); err != nil {
		t.Fatalf("writing the test answers volume: %v", err)
	}
	return path
}

// The omarchy answer set, with the real names — long, lower-case, and mixed
// extension/no-extension, which is precisely the shape plain ISO9660 mangles.
var seedFixture = map[string]string{
	"user_configuration.json":       `{"hostname":"omarchy","version":"3.0.9"}`,
	"user_credentials.json":         `{"users":[{"username":"user","sudo":true}]}`,
	"user_full_name.txt":            "Charly\n",
	"user_email_address.txt":        "charly@opencharly.invalid\n",
	"authorized_keys":               "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI test@charly\n",
	"user_encrypt_installation.txt": "false\n",
}

func TestSeedVolumeFiles_ReadsTheAuthoredNames(t *testing.T) {
	iso := writeTestSeed(t, seedFixture)
	got, err := SeedVolumeFiles(iso)
	if err != nil {
		t.Fatalf("SeedVolumeFiles: %v", err)
	}
	for want := range seedFixture {
		found := false
		for _, g := range got {
			if g == want {
				found = true
				break
			}
		}
		if !found {
			// This is the negative control for the Rock Ridge branch: with rockRidgeName
			// removed the reader reports USER_CONFIGURATION.JSON (or a truncation of it),
			// and the operator cannot ask for the file by the name charly wrote.
			t.Errorf("answers volume does not list %q by its authored name; got %v", want, got)
		}
	}
	if len(got) != len(seedFixture) {
		t.Errorf("expected %d files, got %d: %v", len(seedFixture), len(got), got)
	}
}

func TestSeedVolumeFile_IsByteExact(t *testing.T) {
	iso := writeTestSeed(t, seedFixture)
	for name, want := range seedFixture {
		got, err := SeedVolumeFile(iso, name)
		if err != nil {
			t.Fatalf("SeedVolumeFile(%q): %v", name, err)
		}
		// Byte-exact, not "contains": a seed that is right except for a trailing newline
		// is a seed archinstall rejects, and the whole value of this verb is that what it
		// prints is what the installer reads.
		if string(got) != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestSeedVolumeFile_MissingFileNamesWhatIsThere(t *testing.T) {
	iso := writeTestSeed(t, seedFixture)
	_, err := SeedVolumeFile(iso, "user_credentials.jsonn")
	if err == nil {
		t.Fatal("expected an error for a file that is not on the volume")
	}
	// A bare "not found" is what makes an operator conclude the renderer dropped the file.
	// Listing the volume's real contents in the same breath is the difference between a
	// dead end and an answer.
	if !strings.Contains(err.Error(), "user_credentials.json") {
		t.Errorf("the error must name what the volume DOES hold, got: %v", err)
	}
}

func TestSeedVolumeFiles_RejectsANonISO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "not-an-iso.bin")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0}, 64*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedVolumeFiles(p); err == nil {
		t.Fatal("a file with no CD001 signature must be rejected, not read as an empty volume")
	}
}

// ─── keys ──────────────────────────────────────────────────────────────────────────────

func TestTextToGuestKeys_RealCommandLine(t *testing.T) {
	// The literal command this verb was written to replace typing by hand.
	got, err := TextToGuestKeys("systemctl status sshd -l")
	if err != nil {
		t.Fatalf("TextToGuestKeys: %v", err)
	}
	want := []string{
		"s", "y", "s", "t", "e", "m", "c", "t", "l", "spc",
		"s", "t", "a", "t", "u", "s", "spc", "s", "s", "h", "d", "spc", "minus", "l",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTextToGuestKeys_ShiftedAndUpper(t *testing.T) {
	got, err := TextToGuestKeys(`A:/tmp/x_1?`)
	if err != nil {
		t.Fatalf("TextToGuestKeys: %v", err)
	}
	want := []string{"shift-a", "shift-semicolon", "slash", "t", "m", "p", "slash", "x",
		"shift-minus", "1", "shift-slash"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("key %d: got %v, want %v", i, got, want)
		}
	}
}

// NEGATIVE CONTROL. The tempting implementation skips a character it cannot type. That
// silently runs a DIFFERENT command on the guest than the operator asked for, and nothing
// on either side shows it happened — the worst possible outcome for a verb whose only job
// is to tell you the truth about a guest.
func TestTextToGuestKeys_UntypeableCharacterIsAnError(t *testing.T) {
	for _, s := range []string{"echo héllo", "echo \x01", "grep — dash"} {
		if _, err := TextToGuestKeys(s); err == nil {
			t.Errorf("%q: a character that cannot be typed must be an error, never a silent drop", s)
		}
	}
}

func TestGuestKeyToVirsh(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ret", []string{"KEY_ENTER"}},
		{"spc", []string{"KEY_SPACE"}},
		{"a", []string{"KEY_A"}},
		{"shift-a", []string{"KEY_LEFTSHIFT", "KEY_A"}},
		{"alt-f2", []string{"KEY_LEFTALT", "KEY_F2"}},
		{"ctrl-alt-delete", []string{"KEY_LEFTCTRL", "KEY_LEFTALT", "KEY_DELETE"}},
		{"minus", []string{"KEY_MINUS"}},
		{"grave_accent", []string{"KEY_GRAVE"}},
	}
	for _, c := range cases {
		got, err := guestKeyToVirsh(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

// NEGATIVE CONTROL. `virsh send-key` accepts an unknown keycode name by FAILING with a
// message about the domain, which reads like the guest is gone. Rejecting it here names
// the actual mistake.
func TestGuestKeyToVirsh_UnknownKeyIsAnError(t *testing.T) {
	for _, k := range []string{"enter", "space", "", "ctrl-enter", "f1x"} {
		if _, err := guestKeyToVirsh(k); err == nil {
			t.Errorf("%q: an unknown key name must be rejected before it reaches virsh", k)
		}
	}
}

// Every key TextToGuestKeys can produce must be translatable for the libvirt backend.
// Without this the two halves can drift and `vm type` fails midway through a line,
// leaving a half-typed command at a root prompt.
func TestEveryTypeableKeyTranslatesToVirsh(t *testing.T) {
	var printable strings.Builder
	for r := rune(32); r < 127; r++ {
		printable.WriteRune(r)
	}
	keys, err := TextToGuestKeys(printable.String() + "\n\t")
	if err != nil {
		t.Fatalf("the printable ASCII range must be typeable: %v", err)
	}
	for _, k := range keys {
		if _, err := guestKeyToVirsh(k); err != nil {
			t.Errorf("%q is producible by `vm type` but not translatable for libvirt: %v", k, err)
		}
	}
}

// ─── screenshot conversion ─────────────────────────────────────────────────────────────

func makePPM(w, h int, header string) []byte {
	var b bytes.Buffer
	if header == "" {
		header = "P6\n%d %d\n255\n"
		b.WriteString(strings.Replace(strings.Replace(header, "%d", itoa(w), 1), "%d", itoa(h), 1))
	} else {
		b.WriteString(header)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			b.Write([]byte{byte(x), byte(y), 0x7f})
		}
	}
	return b.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func TestPpmFileToPNG_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "screen.ppm")
	if err := os.WriteFile(src, makePPM(7, 5, ""), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "screen.png")
	if err := ppmFileToPNG(src, dst); err != nil {
		t.Fatalf("ppmFileToPNG: %v", err)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("the verb did not write a decodable PNG: %v", err)
	}
	if got := img.Bounds(); got != image.Rect(0, 0, 7, 5) {
		t.Fatalf("dimensions lost in conversion: %v", got)
	}
	// Pixel fidelity, not just "a PNG appeared": a screenshot whose colours are wrong is
	// worse than none, because it is used to read text off a console.
	r, g, b, a := img.At(3, 2).RGBA()
	if r>>8 != 3 || g>>8 != 2 || b>>8 != 0x7f || a>>8 != 0xff {
		t.Fatalf("pixel (3,2) = %d,%d,%d,%d — conversion is not faithful", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestPpmFileToPNG_CommentsInHeader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "screen.ppm")
	body := makePPM(4, 3, "P6\n# written by qemu\n4 3\n255\n")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ppmFileToPNG(src, filepath.Join(dir, "out.png")); err != nil {
		t.Fatalf("a commented PPM header must be accepted: %v", err)
	}
}

// NEGATIVE CONTROL. A truncated capture — the monitor was interrupted mid-write — must be
// an error. Padding it out produces a plausible-looking screenshot with a black band, and
// an operator reading a console off that image draws a conclusion from data that was never
// captured.
func TestPpmFileToPNG_TruncatedCaptureIsAnError(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "screen.ppm")
	full := makePPM(8, 8, "")
	if err := os.WriteFile(src, full[:len(full)-40], 0o644); err != nil {
		t.Fatal(err)
	}
	err := ppmFileToPNG(src, filepath.Join(dir, "out.png"))
	if err == nil {
		t.Fatal("a truncated capture must be an error, not a partially black screenshot")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("the error must say the capture was truncated, got: %v", err)
	}
}

func TestPpmFileToPNG_RejectsAnotherFormat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "screen.ppm")
	// P3 is ASCII PPM — a real thing some tools emit, and byte-incompatible.
	if err := os.WriteFile(src, []byte("P3\n2 2\n255\n0 0 0 "), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ppmFileToPNG(src, filepath.Join(dir, "out.png")); err == nil {
		t.Fatal("a non-P6 capture must be rejected rather than misread")
	}
}
