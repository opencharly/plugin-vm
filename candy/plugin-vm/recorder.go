package vm

// recorder.go — the DETACHED host-side session recorder (plan Cutover E, E-2).
// A libvirt: session start hands THIS binary (in recorder mode, env
// CHARLY_LIBVIRT_RECORDER=1) to the runner's generic background-session service
// (plugin-check's compiled-in verb:session seam). The recorder owns the libvirt
// RPC for the whole session: it dials the host-pre-resolved libvirt endpoint
// (domain + URI), polls the VM framebuffer at the session fps into
// <state_dir>/frames.mjpeg (each poll is one libvirt DomainScreenshot decoded to
// an image and encoded as a JPEG — the libvirt analogue of the spice record
// loop's every-poll capture), and on SIGTERM/SIGINT finalizes: the deterministic
// FINAL marker + the evidence row.json ("instrument"/"origin"/"verb"/"artifact"
// — the shared #EvidenceRow shape, plan §4 A-task-1). While it runs, the
// PROVIDER spawns no process, knows no transport, and owns no pidfile.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// Recorder-mode env contract between provider.go (the spawn env) and cmd/serve's
// recorder mode (the reader). The provider builds these keys in buildSessionSpawn.
const (
	EnvRecorder  = "CHARLY_LIBVIRT_RECORDER"
	EnvEndpoint  = "CHARLY_LIBVIRT_ENDPOINT"
	EnvFps       = "CHARLY_LIBVIRT_FPS"
	EnvStateDir  = "CHARLY_LIBVIRT_STATE_DIR"
	EnvSessionID = "CHARLY_LIBVIRT_SESSION_ID"
	EnvVenue     = "CHARLY_LIBVIRT_VENUE"
	EnvPhase     = "CHARLY_LIBVIRT_PHASE"
)

// framesFile is the MJPEG artifact name inside the session state dir; finalMarker
// is the deterministic end-of-stream marker the stop path greps for.
const (
	framesFile   = "frames.mjpeg"
	finalMarker  = "FINAL"
	evidenceFile = "row.json"
)

// evidenceRow mirrors the shared #EvidenceRow shape (plan §4 A-task-1) — the
// minimal session subset the recorder writes and sessionStop reads back. No
// plugin-specific manifest code: the runner's evidence phase consumes the general
// shape.
type evidenceRow struct {
	Instrument string             `json:"instrument"`
	Origin     string             `json:"origin"`
	Verb       string             `json:"verb"`
	Venue      string             `json:"venue,omitempty"`
	Phase      string             `json:"phase,omitempty"`
	Artifact   []evidenceArtifact `json:"artifact,omitempty"`
}

type evidenceArtifact struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// vmEndpoint is the resolved session endpoint the provider threads via
// CHARLY_LIBVIRT_ENDPOINT: the libvirt domain to poll + the connection URI ("" =
// qemu:///session — the exact resolution the plugin's own libvirt verbs use). The
// recorder re-dials libvirt detached.
type vmEndpoint struct {
	Domain string `json:"domain"`
	URI    string `json:"uri,omitempty"`
	Screen int    `json:"screen,omitempty"`
}

// ParseEndpoint decodes the endpoint JSON the provider threads via
// CHARLY_LIBVIRT_ENDPOINT (the vmEndpoint wire shape).
func ParseEndpoint(raw []byte) (*vmEndpoint, error) {
	var ep vmEndpoint
	if err := json.Unmarshal(raw, &ep); err != nil {
		return nil, fmt.Errorf("decode endpoint JSON: %w", err)
	}
	return &ep, nil
}

// RecorderConfig is the detached-session recorder's full runtime contract.
type RecorderConfig struct {
	Endpoint  *vmEndpoint
	Fps       int    // frames/second; 0 defaults to 5 (mirrors the record-loop default)
	StateDir  string // the run's state dir: frames.mjpeg + FINAL + row.json land here
	SessionID string // the venue-scoped session id — stamped into the evidence row
	Venue     string // evidence-row provenance
	Phase     string // evidence-row provenance (build|live|update|teardown)
}

// frameSource is the subset of the libvirt screenshot path the recorder consumes:
// one full display poll. libvirtFrameSource satisfies it directly; the poll loop is
// unit-testable with a fake (B12).
type frameSource interface {
	Screenshot() (image.Image, error)
}

// libvirtFrameSource wraps the go-libvirt connection + domain as a frameSource
// over the plugin's existing DomainScreenshot drain (libvirt_ops.go), including
// its virsh fallback for the virtio-gpu RPC-stream framing break.
type libvirtFrameSource struct {
	l      *libvirt.Libvirt
	dom    libvirt.Domain
	screen uint
}

func (s *libvirtFrameSource) Screenshot() (image.Image, error) {
	return captureDomainScreenshot(s.l, s.dom, s.screen)
}

// RunSessionRecorder is the detached-mode engine (cmd/serve, recorder mode): dials
// the host-pre-resolved libvirt endpoint, polls the framebuffer into frames.mjpeg
// until done closes, then finalizes the FINAL marker + row.json. Returns the
// captured frame count.
func RunSessionRecorder(cfg RecorderConfig, done <-chan struct{}) (int, error) {
	if cfg.Endpoint == nil {
		return 0, fmt.Errorf("recorder: nil endpoint")
	}
	conn, err := connectLibvirt(cfg.Endpoint.URI)
	if err != nil {
		return 0, fmt.Errorf("recorder: connect libvirt: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	dom, err := conn.lookupDomain(cfg.Endpoint.Domain)
	if err != nil {
		return 0, fmt.Errorf("recorder: domain %q: %w", cfg.Endpoint.Domain, err)
	}
	// Running gate: DomainScreenshot against a shut-off domain fails; fail fast
	// with a clear state instead of a stream error mid-session.
	if st, serr := conn.domainState(dom); serr == nil && st != libvirt.DomainRunning {
		return 0, fmt.Errorf("recorder: domain %q not running (state %s)", cfg.Endpoint.Domain, domainStateString(st))
	}
	src := &libvirtFrameSource{l: conn.l, dom: dom, screen: uint(cfg.Endpoint.Screen)}
	return captureSession(src, cfg, done)
}

// captureSession is the recorder core, unit-testable with the frame fake: polls
// the framebuffer source at the session fps, appending every frame as a JPEG into
// <state_dir>/frames.mjpeg until done closes, then finalizes. Returns the frame
// count.
func captureSession(s frameSource, cfg RecorderConfig, done <-chan struct{}) (int, error) {
	if cfg.StateDir == "" {
		return 0, fmt.Errorf("recorder: empty state dir")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o755); err != nil {
		return 0, fmt.Errorf("recorder: create state dir: %w", err)
	}
	out, err := os.Create(filepath.Join(cfg.StateDir, framesFile))
	if err != nil {
		return 0, fmt.Errorf("recorder: open %s: %w", framesFile, err)
	}
	count := writeFrames(s, captureInterval(cfg.Fps), out, done)
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("recorder: close %s: %w", framesFile, err)
	}
	if err := finalizeSession(cfg, count); err != nil {
		return 0, err
	}
	return count, nil
}

// captureInterval maps a fps int to a poll interval (default 5 fps; 100 fps cap —
// identical semantics to the record loop's interval).
func captureInterval(fps int) time.Duration {
	if fps <= 0 {
		fps = 5
	}
	d := time.Second / time.Duration(fps)
	if d < 10*time.Millisecond {
		d = 10 * time.Millisecond
	}
	return d
}

// writeFrames polls the framebuffer source at interval, appending each frame as a
// JPEG onto w until done closes. Video semantics identical to the record loop:
// every poll is one frame of the stream (a full DomainScreenshot per frame; a
// poll that errors is skipped — a broken display just stops appending). Returns
// the frame count.
func writeFrames(s frameSource, interval time.Duration, w io.Writer, done <-chan struct{}) int {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	count := 0
	for {
		select {
		case <-done:
			return count
		case <-tick.C:
			img, err := s.Screenshot()
			if err != nil || img == nil {
				continue
			}
			b := encodeFrame(img)
			if len(b) == 0 {
				continue
			}
			w.Write(b)
			count++
		}
	}
}

// finalizeSession writes the deterministic end-of-stream marker + the evidence row
// into the state dir. Called once, on the SIGTERM path — the runner's stop is
// complete only when row.json is on disk.
func finalizeSession(cfg RecorderConfig, count int) error {
	marker := fmt.Sprintf("final frames=%d\n", count)
	if err := os.WriteFile(filepath.Join(cfg.StateDir, finalMarker), []byte(marker), 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", finalMarker, err)
	}
	row := evidenceRow{
		Instrument: cfg.SessionID,
		Origin:     "session",
		Verb:       "libvirt",
		Venue:      cfg.Venue,
		Phase:      cfg.Phase,
		Artifact: []evidenceArtifact{{
			Path: filepath.Join(cfg.StateDir, framesFile),
			Kind: "mjpeg",
		}},
	}
	b, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return fmt.Errorf("recorder: marshal evidence row: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(cfg.StateDir, evidenceFile), b, 0o644); err != nil {
		return fmt.Errorf("recorder: write %s: %w", evidenceFile, err)
	}
	return nil
}

// encodeFrame encodes one display frame as a JPEG — the recorder's ONE encoder
// (a nil return means the encoder rejected the frame).
func encodeFrame(img image.Image) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return b.Bytes()
}
