package vm

// vm_diagnose.go — the verbs for a guest you CANNOT ssh into.
//
// Every one of these replaces something that was being done BY HAND during a real
// investigation, with `virsh`, `socat`, `isoinfo` and ImageMagick. That is a product defect
// in itself: the moment charly cannot answer a question about its own VM, the operator
// leaves charly, and the answer stops being reproducible — nobody can re-run it, a bed
// cannot assert it, and the next person starts from zero. R4a — the missing capability is
// the bug.
//
// What they answer, in the order an investigation needs them:
//
//	screenshot  what is on the guest's screen RIGHT NOW (it may have no sshd, no network,
//	            or be sitting in an initramfs prompt)
//	sendkey     drive that screen — switch VTs, log in, run one command — when SSH is the
//	            thing that is broken
//	type        the same, for a whole string, so a diagnosis is one line not forty keys
//	seed        what is actually ON the answers volume charly rendered, byte for byte
//
// None of them require the guest to be reachable, which is the entire point. All of them
// work on BOTH backends: `virsh` for libvirt, the QMP monitor for qemu — the same split
// `vm console` already makes, because a verb that silently works on one backend only is
// a half-cutover, not a capability.

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// qmpDialTimeout bounds every monitor interaction. A hung monitor is itself a finding, and
// a verb that blocks forever hides it.
const qmpDialTimeout = 15 * time.Second

// ─── screenshot ────────────────────────────────────────────────────────────────────────

type VmScreenshotCmd struct {
	Box      string `arg:"" help:"Box or kind:vm entity name"`
	Instance string `short:"i" name:"instance" help:"Instance name"`
	Domain   string `name:"domain" help:"Per-deploy domain identity (screenshot charly-<domain>); absent for a direct screenshot (domain = entity)."`
	Out      string `short:"o" name:"out" help:"Write the PNG here (default: ./<vm>-<timestamp>.png)"`
}

func (c *VmScreenshotCmd) Run() error {
	name := vmName(domainOr(c.Box, c.Domain), c.Instance)
	backend, err := vmBackendFor(c.Box)
	if err != nil {
		return err
	}
	out := c.Out
	if out == "" {
		out = fmt.Sprintf("%s-%s.png", name, time.Now().Format("20060102-150405"))
	}

	// Both backends emit PPM. Converting it here rather than telling the operator to install
	// ImageMagick: a diagnostic that needs a second tool is a diagnostic people skip.
	tmp, err := os.CreateTemp("", "charly-screenshot-*.ppm")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	switch backend {
	case "qemu":
		if err := qemuScreendump(name, tmpPath); err != nil {
			return err
		}
	default:
		if err := virshScreenshot(name, tmpPath); err != nil {
			return err
		}
	}

	if err := capturedScreenToPNG(tmpPath, out); err != nil {
		return err
	}
	fmt.Printf("%s\n", out)
	return nil
}

// capturedScreenToPNG normalizes whatever the backend wrote into a PNG at dst.
//
// The format is NOT fixed and must be SNIFFED, not assumed — found by running this verb
// against a real domain rather than by reading a man page. `virsh screenshot` returns
// whatever the device's framebuffer stream is: current libvirt with a virtio-vga head hands
// back a PNG already, while qemu's own `screendump` writes PPM. Assuming either one turns a
// working capture into a hard error on half the guests, and assuming it the OTHER way
// (trusting the extension) writes a file that no viewer can open.
func capturedScreenToPNG(src, dst string) error {
	magic, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading the captured screen: %w", err)
	}
	switch {
	case len(magic) >= 8 && bytes.HasPrefix(magic, []byte("\x89PNG\r\n\x1a\n")):
		// Already the target format — re-encoding would only lose fidelity.
		return os.WriteFile(dst, magic, 0o644)
	case bytes.HasPrefix(magic, []byte("P6")):
		return ppmFileToPNG(src, dst)
	case len(magic) == 0:
		return fmt.Errorf("the backend wrote an EMPTY capture to %s — the guest produced no frame", src)
	default:
		n := 8
		if len(magic) < n {
			n = len(magic)
		}
		return fmt.Errorf("captured screen starts with %q — neither PNG nor binary PPM (P6); "+
			"the backend's screenshot format is not one this verb can convert", magic[:n])
	}
}

func virshScreenshot(domain, dst string) error {
	virsh, err := exec.LookPath("virsh")
	if err != nil {
		return fmt.Errorf("virsh is required to capture a libvirt guest's screen: %w", err)
	}
	cmd := exec.Command(virsh, "-c", libvirtSessionURI, "screenshot", domain, dst)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("virsh screenshot %s: %v: %s", domain, err, noScreenHint(domain, errBuf.String()))
	}
	return nil
}

func qemuScreendump(domain, dst string) error {
	sock, err := vmMonitorSocket(domain, "qmp.sock")
	if err != nil {
		return err
	}
	q, err := dialQMP(sock, qmpDialTimeout)
	if err != nil {
		return err
	}
	defer q.Close() //nolint:errcheck
	// qemu writes the file itself, so it must be an ABSOLUTE path — a relative one lands in
	// qemu's cwd, which is not the operator's, and the verb would then report success over a
	// file that is not there.
	abs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	out, err := q.humanMonitor("screendump " + abs)
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(out); s != "" {
		return fmt.Errorf("screendump %s: %s", domain, noScreenHint(domain, s))
	}
	return nil
}

// noScreenHint names the single most common cause rather than leaving it to guesswork: a
// domain defined with no video device cannot be screenshotted, and a `libvirt.devices.video`
// (plus `graphics`) block on the kind:vm entity is the fix.
func noScreenHint(domain, msg string) string {
	msg = strings.TrimSpace(msg)
	if strings.Contains(msg, "no screens") || strings.Contains(msg, "No available console") {
		return msg + " — VM " + domain + " was defined with NO video device; add a " +
			"libvirt.devices.video (and graphics) block to its kind:vm entity"
	}
	return msg
}

// ppmFileToPNG converts the binary PPM (P6) both backends produce into PNG. Written out
// rather than shelling to ImageMagick so the verb adds no dependency charly does not
// already have.
func ppmFileToPNG(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading the captured screen: %w", err)
	}
	defer f.Close() //nolint:errcheck

	r := bufio.NewReader(f)
	magic, err := ppmToken(r)
	if err != nil {
		return err
	}
	if magic != "P6" {
		return fmt.Errorf("captured screen is %q, not a binary PPM (P6)", magic)
	}
	w, err := ppmInt(r)
	if err != nil {
		return err
	}
	h, err := ppmInt(r)
	if err != nil {
		return err
	}
	maxv, err := ppmInt(r)
	if err != nil {
		return err
	}
	if maxv != 255 {
		return fmt.Errorf("captured screen has maxval %d; only 8-bit PPM is supported", maxv)
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("captured screen has degenerate dimensions %dx%d", w, h)
	}
	// NOTE: ppmToken consumes the single whitespace byte that terminates each header token,
	// including the one before the pixel data — so the reader is ALREADY positioned on the
	// first pixel. Consuming another byte here shifts every pixel by one and drops the last
	// row; the round-trip test is the pin for that.

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	row := make([]byte, 3*w)
	for y := 0; y < h; y++ {
		if err := readFull(r, row); err != nil {
			return fmt.Errorf("captured screen is truncated at row %d of %d: %w", y, h, err)
		}
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = row[3*x], row[3*x+1], row[3*x+2], 255
		}
	}

	o, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	defer o.Close() //nolint:errcheck
	return png.Encode(o, img)
}

func readFull(r *bufio.Reader, p []byte) error {
	n := 0
	for n < len(p) {
		m, err := r.Read(p[n:])
		n += m
		if err != nil {
			return err
		}
	}
	return nil
}

// ppmToken reads one whitespace-delimited token, skipping '#' comment lines.
func ppmToken(r *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		b, err := r.ReadByte()
		if err != nil {
			return "", err
		}
		switch {
		case b == '#':
			for b != '\n' {
				if b, err = r.ReadByte(); err != nil {
					return "", err
				}
			}
		case b == ' ' || b == '\t' || b == '\n' || b == '\r':
			if sb.Len() > 0 {
				return sb.String(), nil
			}
		default:
			sb.WriteByte(b)
		}
	}
}

func ppmInt(r *bufio.Reader) (int, error) {
	tok, err := ppmToken(r)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range tok {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("malformed PPM header value %q", tok)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ─── sendkey / type ────────────────────────────────────────────────────────────────────

type VmSendkeyCmd struct {
	Box      string   `arg:"" help:"Box or kind:vm entity name"`
	Keys     []string `arg:"" help:"Key names in qemu-monitor form, e.g. alt-f2 ret ctrl-alt-delete"`
	Instance string   `short:"i" name:"instance" help:"Instance name"`
	Domain   string   `name:"domain" help:"Per-deploy domain identity; absent for a direct send (domain = entity)."`
}

func (c *VmSendkeyCmd) Run() error {
	return sendGuestKeys(c.Box, domainOr(c.Box, c.Domain), c.Instance, c.Keys)
}

type VmTypeCmd struct {
	Box      string `arg:"" help:"Box or kind:vm entity name"`
	Text     string `arg:"" help:"Text to type on the guest console"`
	Instance string `short:"i" name:"instance" help:"Instance name"`
	Domain   string `name:"domain" help:"Per-deploy domain identity; absent for a direct type (domain = entity)."`
	NoEnter  bool   `name:"no-enter" help:"Do not press Enter after the text"`
}

func (c *VmTypeCmd) Run() error {
	keys, err := TextToGuestKeys(c.Text)
	if err != nil {
		return err
	}
	if !c.NoEnter {
		keys = append(keys, "ret")
	}
	return sendGuestKeys(c.Box, domainOr(c.Box, c.Domain), c.Instance, keys)
}

// sendGuestKeys opens ONE monitor connection for the whole burst. Per-key connections are
// what makes hand-driven `socat` typing unusable — the reconnect between keystrokes is
// slower than the guest's own key repeat and reorders characters.
func sendGuestKeys(entity, domain, instance string, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("no keys to send")
	}
	name := vmName(domain, instance)
	backend, err := vmBackendFor(entity)
	if err != nil {
		return err
	}
	if backend == "qemu" {
		sock, err := vmMonitorSocket(name, "qmp.sock")
		if err != nil {
			return err
		}
		q, err := dialQMP(sock, qmpDialTimeout)
		if err != nil {
			return err
		}
		defer q.Close() //nolint:errcheck
		for _, k := range keys {
			// The monitor's own key names ARE this package's vocabulary, so nothing is
			// translated here; qemu reports an unknown key in the reply text with a zero
			// status, which is why that text is checked rather than dropped.
			out, err := q.humanMonitor("sendkey " + k)
			if err != nil {
				return err
			}
			if s := strings.TrimSpace(out); s != "" {
				return fmt.Errorf("sendkey %q to %s: %s", k, name, s)
			}
		}
		return nil
	}

	virsh, err := exec.LookPath("virsh")
	if err != nil {
		return fmt.Errorf("virsh is required to send keys to a libvirt guest: %w", err)
	}
	for _, k := range keys {
		codes, err := guestKeyToVirsh(k)
		if err != nil {
			return err
		}
		args := append([]string{"-c", libvirtSessionURI, "send-key", name}, codes...)
		cmd := exec.Command(virsh, args...)
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("send-key %q to %s: %v: %s", k, name, err, strings.TrimSpace(errBuf.String()))
		}
	}
	return nil
}

// ─── seed ──────────────────────────────────────────────────────────────────────────────

type VmSeedCmd struct {
	Ls  VmSeedLsCmd  `cmd:"" help:"List the files on a VM's rendered answers volume"`
	Cat VmSeedCatCmd `cmd:"" help:"Print one file from a VM's rendered answers volume"`
}

type VmSeedLsCmd struct {
	Box      string `arg:"" help:"Box or kind:vm entity name"`
	Instance string `short:"i" name:"instance" help:"Instance name"`
	Domain   string `name:"domain" help:"Per-deploy domain identity — reads THAT domain's seed, which differs from the entity's (per-domain ssh key)."`
}

func (c *VmSeedLsCmd) Run() error {
	path, err := seedPathFor(c.Box, c.Domain, c.Instance)
	if err != nil {
		return err
	}
	names, err := SeedVolumeFiles(path)
	if err != nil {
		return err
	}
	fmt.Printf("# %s\n", path)
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

type VmSeedCatCmd struct {
	Box      string `arg:"" help:"Box or kind:vm entity name"`
	File     string `arg:"" help:"File on the answers volume, e.g. user_configuration.json"`
	Instance string `short:"i" name:"instance" help:"Instance name"`
	Domain   string `name:"domain" help:"Per-deploy domain identity."`
}

func (c *VmSeedCatCmd) Run() error {
	path, err := seedPathFor(c.Box, c.Domain, c.Instance)
	if err != nil {
		return err
	}
	body, err := SeedVolumeFile(path, c.File)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(body)
	return err
}

// seedPathFor resolves WHICH answers volume to read: the per-domain one when --domain is
// given, else the entity's build output. They differ — the per-domain seed carries that
// domain's ssh public key, which is the whole reason `vm create` re-packs it — and reading
// the wrong one is exactly the class of mistake these verbs exist to prevent.
func seedPathFor(box, domain, instance string) (string, error) {
	if domain != "" {
		base, err := vmDir()
		if err != nil {
			return "", err
		}
		p := filepath.Join(base, vmName(domain, instance), "seed.iso")
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("no per-domain answers volume at %s — has `charly vm create --domain %s` run?", p, domain)
		}
		return p, nil
	}
	p := filepath.Join(vmDiskDir(box), "seed.iso")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no answers volume at %s — has `charly vm build %s` run?", p, box)
	}
	return p, nil
}

// ─── shared ────────────────────────────────────────────────────────────────────────────

// vmBackendFor resolves the entity's backend the same way `vm console` does. A resolution
// failure falls back to libvirt — the default backend — rather than aborting: these verbs
// are used when things are already broken, and refusing to look at a guest because its
// entity no longer resolves is the least useful moment to be strict.
func vmBackendFor(entity string) (string, error) {
	reply, err := hostConfigResolve(entity)
	if err != nil {
		return "libvirt", nil //nolint:nilerr // see comment above: degrade, do not block a diagnosis
	}
	if reply.Backend == "" {
		return "libvirt", nil
	}
	return reply.Backend, nil
}

func vmMonitorSocket(domainName, file string) (string, error) {
	dir, err := vmDir()
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, domainName, file)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("no monitor socket at %s — is %s running?", p, domainName)
	}
	return p, nil
}
