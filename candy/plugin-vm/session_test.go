package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencharly/plugin-vm/candy/plugin-vm/params"
	"github.com/opencharly/spec/spec"
)

// session_test.go covers the libvirt: session method (plan Cutover E, E-2): the
// recorder loop (framebuffer polling → frames.mjpeg, finalize → FINAL + row.json),
// the spawn request the provider hands to the runner's generic background-session
// service, and the runSession validation gates. The reverse-leg submission itself
// (InvokeProvider ClassVerb session) is exercised by the Cutover E R10 bed — the
// venue-driving path.

// TestCaptureSessionWritesFramesAndFinalizes is the recorder-loop unit test with
// the frame fake: polling frames land in <state_dir>/frames.mjpeg, and closing the
// done channel (the SIGTERM trap's analog) finalizes with the FINAL marker + the
// evidence row.json.
func TestCaptureSessionWritesFramesAndFinalizes(t *testing.T) {
	stateDir := t.TempDir()
	fake := &fakeFrameSource{img: solidRGBA(32, 32, color.RGBA{R: 90, G: 140, B: 200, A: 255})}
	done := make(chan struct{})
	cfg := RecorderConfig{
		Fps:       100, // 10ms poll interval
		StateDir:  stateDir,
		SessionID: "bed.member.cap",
		Venue:     "check-some-vm",
		Phase:     "live",
	}
	type res struct {
		count int
		err   error
	}
	rc := make(chan res, 1)
	go func() {
		c, err := captureSession(fake, cfg, done)
		rc <- res{c, err}
	}()
	// Let a few poll intervals elapse, then close done (the SIGTERM analog).
	time.Sleep(45 * time.Millisecond)
	close(done)
	got := <-rc
	if got.err != nil {
		t.Fatalf("captureSession: %v", got.err)
	}
	if got.count < 2 {
		t.Fatalf("captured %d frames, want >= 2 (poll loop -> frames)", got.count)
	}

	mjpeg, err := os.ReadFile(filepath.Join(stateDir, framesFile))
	if err != nil {
		t.Fatalf("frames.mjpeg: %v", err)
	}
	if frames := splitMJpeg(mjpeg); len(frames) != got.count {
		t.Errorf("splitMJpeg frames = %d, want %d", len(frames), got.count)
	}

	marker, err := os.ReadFile(filepath.Join(stateDir, finalMarker))
	if err != nil {
		t.Fatalf("FINAL marker missing after stop: %v", err)
	}
	if want := "final frames=" + itoa(got.count); string(marker) != want+"\n" {
		t.Errorf("FINAL content = %q, want %q", marker, want)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, evidenceFile))
	if err != nil {
		t.Fatalf("row.json missing after stop: %v", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatalf("decode row.json: %v", err)
	}
	tw := evidenceRow{
		Instrument: "bed.member.cap",
		Origin:     "session",
		Verb:       "libvirt",
		Venue:      "check-some-vm",
		Phase:      "live",
		Artifact:   []evidenceArtifact{{Path: filepath.Join(stateDir, framesFile), Kind: "mjpeg"}},
	}
	if row.Instrument != tw.Instrument || row.Origin != tw.Origin || row.Verb != tw.Verb ||
		row.Venue != tw.Venue || row.Phase != tw.Phase || len(row.Artifact) != 1 ||
		row.Artifact[0].Path != tw.Artifact[0].Path || row.Artifact[0].Kind != tw.Artifact[0].Kind {
		t.Errorf("row.json = %+v, want %+v", row, tw)
	}
}

// TestCaptureSessionEmptyStateDir guards the recorder's honest failure when the
// spawn env left no state dir.
func TestCaptureSessionEmptyStateDir(t *testing.T) {
	if _, err := captureSession(&fakeFrameSource{}, RecorderConfig{}, make(chan struct{})); err == nil {
		t.Fatal("captureSession with empty state dir: want error")
	}
}

// TestResolveRecorderExe guards the recorder-binary resolution the live run exposed:
// plugin-vm's canonical placement is COMPILED-IN, where os.Executable() is the CHARLY
// binary — which carries no recorder shim (the shim lives in this plugin's serve
// binary). The resolution must prefer the plugin's serve twin on the baked-plugin
// search path ($CHARLY_PLUGIN_DIR first, then the FHS /usr/lib/charly/plugins;
// $CHARLY_PLUGIN_ONLY=1 drops the FHS leg — the loader's own semantics), falling back
// to the current binary only when no twin exists (the out-of-process placement, where
// the current binary IS the twin).
func TestResolveRecorderExe(t *testing.T) {
	// 1) The twin on $CHARLY_PLUGIN_DIR wins (the first-hit precedence).
	dir := t.TempDir()
	twin := filepath.Join(dir, "plugin-vm")
	if err := os.WriteFile(twin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write twin: %v", err)
	}
	t.Setenv("CHARLY_PLUGIN_DIR", dir)
	if got := resolveRecorderExe(); got != twin {
		t.Errorf("resolveRecorderExe with twin on CHARLY_PLUGIN_DIR = %q, want %q", got, twin)
	}
	// 2) CHARLY_PLUGIN_DIR set but WITHOUT the twin: the FHS leg is skipped under
	// CHARLY_PLUGIN_ONLY=1 (deterministic), so the current binary is the fallback.
	t.Setenv("CHARLY_PLUGIN_DIR", filepath.Join(t.TempDir()))
	t.Setenv("CHARLY_PLUGIN_ONLY", "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if got := resolveRecorderExe(); got != self {
		t.Errorf("resolveRecorderExe with empty dir + PLUGIN_ONLY = %q, want fallback %q", got, self)
	}
	// 3) An explicit dir listing a NON-EXECUTABLE/garbage twin still resolves by
	// presence (os.Stat only — the runner owns exec failure reporting).
	t.Setenv("CHARLY_PLUGIN_ONLY", "")
	bd := t.TempDir()
	bad := filepath.Join(bd, "plugin-vm")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plain twin: %v", err)
	}
	t.Setenv("CHARLY_PLUGIN_DIR", bd)
	if got := resolveRecorderExe(); got != bad {
		t.Errorf("resolveRecorderExe = %q, want %q (presence-only resolution)", got, bad)
	}
}

// TestBuildSessionSpawn asserts the exact spawn request the provider submits to the
// runner's generic session service: this plugin's binary in recorder mode + the
// endpoint/identity env, with the venue default from the CheckEnv snapshot applied.
func TestBuildSessionSpawn(t *testing.T) {
	ep := &vmEndpoint{Domain: "charly-cachyos", URI: "qemu:///session"}
	in := &params.LibvirtVerbInput{SessionId: "bed.member.cap", StateDir: "/var/run/checks/bed/x", Fps: 5, Phase: "live"}
	req := buildSessionSpawn(in, ep, "/usr/lib/charly/plugin-vm", "check-some-vm", "")
	if req.Op != "spawn" {
		t.Errorf("op = %q, want spawn", req.Op)
	}
	if req.SessionID != "bed.member.cap" {
		t.Errorf("session_id = %q", req.SessionID)
	}
	if len(req.Command) != 2 || req.Command[0] != "/usr/lib/charly/plugin-vm" || req.Command[1] != "__dummy-arg" {
		t.Errorf("command = %v, want [<self> __dummy-arg]", req.Command)
	}
	if req.Env[EnvRecorder] != "1" {
		t.Errorf("CHARLY_LIBVIRT_RECORDER = %q, want 1", req.Env[EnvRecorder])
	}
	if req.Env[EnvFps] != "5" {
		t.Errorf("CHARLY_LIBVIRT_FPS = %q, want 5", req.Env[EnvFps])
	}
	if req.Env[EnvStateDir] != "/var/run/checks/bed/x" {
		t.Errorf("CHARLY_LIBVIRT_STATE_DIR = %q", req.Env[EnvStateDir])
	}
	if req.Env[EnvSessionID] != "bed.member.cap" {
		t.Errorf("CHARLY_LIBVIRT_SESSION_ID = %q", req.Env[EnvSessionID])
	}
	if req.Env[EnvVenue] != "check-some-vm" {
		t.Errorf("CHARLY_LIBVIRT_VENUE = %q, want check-some-vm (CheckEnv default)", req.Env[EnvVenue])
	}
	if req.Env[EnvPhase] != "live" {
		t.Errorf("CHARLY_LIBVIRT_PHASE = %q, want live", req.Env[EnvPhase])
	}
	// the endpoint rides the env as the vmEndpoint JSON (domain + uri).
	var gotEP vmEndpoint
	if err := json.Unmarshal([]byte(req.Env[EnvEndpoint]), &gotEP); err != nil {
		t.Fatalf("CHARLY_LIBVIRT_ENDPOINT not JSON: %v", err)
	}
	if gotEP.Domain != "charly-cachyos" || gotEP.URI != "qemu:///session" {
		t.Errorf("endpoint JSON = %+v", gotEP)
	}
	// fps defaulting: zero fps in the input spawns the 5 fps default.
	def := buildSessionSpawn(&params.LibvirtVerbInput{SessionId: "s", StateDir: "/x"}, ep, "/e", "", "")
	if def.Env[EnvFps] != "5" {
		t.Errorf("default fps = %q, want 5", def.Env[EnvFps])
	}
}

// TestParseEndpoint guards the endpoint JSON decode of the recorder env.
func TestParseEndpoint(t *testing.T) {
	ep, err := ParseEndpoint([]byte(`{"domain":"charly-x","uri":"qemu:///session"}`))
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if ep.Domain != "charly-x" || ep.URI != "qemu:///session" || ep.Screen != 0 {
		t.Errorf("ParseEndpoint = %+v", ep)
	}
	if _, err := ParseEndpoint([]byte("not json")); err == nil {
		t.Error("ParseEndpoint garbage: want error")
	}
}

// TestRunSessionValidation guards the required-modifier semantics of the session
// method WITHOUT the reverse leg: each gate fails before any submission.
func TestRunSessionValidation(t *testing.T) {
	ctx := t.Context()
	// session_id/state_dir have provider-side fallbacks (plan-step sessions); the
	// gates that remain are action validation + the submission dispatch under a stub cc.
	if _, err := runSession(ctx, stubCC{}, nil, &params.LibvirtVerbInput{Action: "bogus"}, ""); err == nil {
		t.Error("bogus action: want error")
	}
	// a well-formed session reaches the submission (the stub cc answers an error -
	// proving the InvokeProvider dispatch path is exercised, not skipped).
	if _, err := runSession(ctx, stubCC{}, nil, &params.LibvirtVerbInput{SessionId: "s", Action: "start"}, ""); err == nil {
		t.Error("start with stub cc: want error (stub answers an error)")
	}
}

type stubCC struct{ spec.CheckContext }

func (stubCC) InvokeProvider(ctx context.Context, class, word, op string, paramsJSON, env []byte) ([]byte, error) {
	return nil, fmt.Errorf("stub: submission reached the reverse-leg dispatch (class %s word %s op %s)", class, word, op)
}

// TestWaitForEvidenceRow covers the stop path's bounded row wait: the row appears
// (the recorder's SIGTERM trap finalizes) → the bytes are returned; the row never
// appears → the deadline error names the path + timeout. The timeout path is the
// crashed-recorder case (no row, ever) — the stop must fail fast, not hang.
func TestWaitForEvidenceRow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rowPath := filepath.Join(dir, evidenceFile)

	// Row-appears path: the row lands after a short delay (the recorder's finalize
	// lagging the stop return) — the wait must return the bytes.
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(rowPath, []byte(`{"instrument":"s"}`), 0o644)
	}()
	raw, err := waitForEvidenceRow(ctx, rowPath, 2*time.Second)
	if err != nil {
		t.Fatalf("row-appears path: %v", err)
	}
	if string(raw) != `{"instrument":"s"}` {
		t.Fatalf("row-appears path: got %q", raw)
	}

	// Timeout path: no row ever lands — the wait must fail with the path + timeout
	// named (the crashed-recorder case).
	missing := filepath.Join(dir, "missing", evidenceFile)
	_, err = waitForEvidenceRow(ctx, missing, 150*time.Millisecond)
	if err == nil {
		t.Fatal("timeout path: want error")
	}
	if !strings.Contains(err.Error(), "evidence row missing after") {
		t.Fatalf("timeout path: error %q does not name the deadline", err)
	}

	// Cancelled-ctx path: a cancelled context aborts the wait promptly.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = waitForEvidenceRow(cctx, missing, 5*time.Second)
	if err == nil {
		t.Fatal("cancelled-ctx path: want error")
	}
}

// helpers ----------------------------------------------------------------

// fakeFrameSource returns a fixed framebuffer — the frameSource fake for the
// recorder-loop test (the go-libvirt DomainScreenshot transport itself is exercised
// by the Cutover E R10 bed).
type fakeFrameSource struct {
	img *image.RGBA
}

func (f *fakeFrameSource) Screenshot() (image.Image, error) { return f.img, nil }

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// splitMJpeg splits a concatenated-JPEG stream at SOI/EOI boundaries.
func splitMJpeg(data []byte) [][]byte {
	var frames [][]byte
	i := 0
	for i < len(data)-1 {
		if data[i] == 0xFF && data[i+1] == 0xD8 { // SOI
			j := i + 2
			for j < len(data)-1 {
				if data[j] == 0xFF && data[j+1] == 0xD9 { // EOI
					frames = append(frames, data[i:j+2])
					i = j + 2
					break
				}
				j++
			}
			if j >= len(data)-1 {
				break
			}
			continue
		}
		i++
	}
	return frames
}

// TestSplitMJpegFramesDecode guards the frame stream quality: every JPEG in the
// MJPEG stream must decode standalone (the transcodable-frames contract).
func TestSplitMJpegFramesDecode(t *testing.T) {
	var buf bytes.Buffer
	for _, c := range []color.RGBA{
		{R: 90, G: 140, B: 200, A: 255},
		{R: 200, G: 90, B: 140, A: 255},
		{R: 140, G: 200, B: 90, A: 255},
	} {
		buf.Write(encodeFrame(solidRGBA(16, 16, c)))
	}
	frames := splitMJpeg(buf.Bytes())
	if len(frames) != 3 {
		t.Fatalf("splitMJpeg = %d frames, want 3", len(frames))
	}
	for i, fr := range frames {
		if _, err := jpeg.Decode(bytes.NewReader(fr)); err != nil {
			t.Errorf("frame %d does not decode: %v", i, err)
		}
	}
}

// TestCaptureInterval guards the fps → interval mapping (5 fps default, 100 fps cap).
func TestCaptureInterval(t *testing.T) {
	if got := captureInterval(0); got != 200*time.Millisecond {
		t.Errorf("captureInterval(0) = %v, want 200ms (5 fps default)", got)
	}
	if got := captureInterval(10); got != 100*time.Millisecond {
		t.Errorf("captureInterval(10) = %v, want 100ms", got)
	}
	if got := captureInterval(200); got != 10*time.Millisecond {
		t.Errorf("captureInterval(200) = %v, want 10ms (100 fps cap)", got)
	}
}
