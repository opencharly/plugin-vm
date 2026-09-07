package vm

// session_method.go — the libvirt: session method (plan Cutover E, E-2): the
// HOST-side detached recorder driven through the runner's GENERIC background-session
// service (plugin-check's compiled-in verb:session seam, candy/plugin-check/
// session_seam.go). The provider submits a GENERIC host-step request — its recorder
// command + session identity, NEVER a systemd unit — and the runner dispatches it;
// the provider never learns the transport, spawns no process, owns no pidfile.
//
// Wire shape (the runner's contract):
//
//	{ "op": "spawn"|"stop"|"status"|"sweep",
//	  "session_id": "<venue-scoped id>",
//	  "command":    ["<recorder argv>"],          // spawn only
//	  "dir":        "<recorder working dir>",     // spawn only
//	  "env":        {"K": "V", ...},              // spawn only
//	  "log_dir":    "<.check/<bed>/<calver>>" }   // the run dir the capture/ state lives in
//
// Submitted over the EXISTING InvokeProvider reverse leg — ClassVerb "session",
// OpExecute — the SAME E3b channel the graphics-endpoint consumers ride, so the
// provider code is placement-invisible (compiled-in vs out-of-process). The
// recorder is THIS plugin's OWN binary in recorder mode (CHARLY_LIBVIRT_RECORDER=1;
// cmd/serve) holding the libvirt RPC — the provider itself never dials for a
// session.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/opencharly/plugin-vm/candy/plugin-vm/params"
	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/ops"
)

// sessionRequest is the wire request the runner's session service decodes
// (plugin-check/candy/plugin-check/session_seam.go, the sessionSeamRequest shape).
type sessionRequest struct {
	Op        string            `json:"op"`
	SessionID string            `json:"session_id"`
	Command   []string          `json:"command,omitempty"`
	Dir       string            `json:"dir,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	LogDir    string            `json:"log_dir,omitempty"`
}

// sessionStatusReply is the runner's liveness answer (the status op, sessionSeamReply).
type sessionStatusReply struct {
	Alive bool `json:"alive,omitempty"`
}

// runSession dispatches the session method against the runner service. The endpoint
// is the PROVIDER-resolved one (the Invoke already resolved the domain + the live
// gate); the recorder re-dials libvirt detached. venueDefault is the CheckEnv
// snapshot's venue id, used when the authored input carries none (evidence-row
// provenance).
func runSession(ctx context.Context, cc kit.CheckContext, ep *vmEndpoint, in *params.LibvirtVerbInput, venueDefault string) (string, error) {
	// session identity: the runner injects session_id for instruments; a PLAN-STEP
	// session (authoring libvirt: {method: session, action: start, session_id: x}
	// directly in a plan) falls back to "default" — the same fallback the
	// record-session contract uses (the spice record_name chain has no analogue:
	// libvirt has no record method).
	if in.SessionId == "" {
		in.SessionId = "default"
	}
	// state_dir is runner-injected for instruments; a plan-step session derives its
	// own under the host temp dir (the row.json + frames live there; instrument
	// sessions always receive the run-dir state dir).
	if in.StateDir == "" {
		in.StateDir = filepath.Join(os.TempDir(), "charly-session", in.SessionId)
	}
	switch in.Action {
	case "start":
		return sessionStart(ctx, cc, ep, in, venueDefault)
	case "stop":
		return sessionStop(ctx, cc, in)
	case "status":
		return sessionStatus(ctx, cc, in)
	}
	return "", fmt.Errorf("session requires action: start|stop|status")
}

// buildSessionSpawn builds the spawn request the runner turns into the detached
// recorder process: THIS plugin's binary in recorder mode with the resolved endpoint
// + session identity threaded via the CHARLY_LIBVIRT_* env (recorder.go). Extracted
// from sessionStart so the wire request is unit-testable without the reverse leg.
func buildSessionSpawn(in *params.LibvirtVerbInput, ep *vmEndpoint, exe, venue, logDir string) sessionRequest {
	fps := in.Fps
	if fps <= 0 {
		fps = 5
	}
	epJSON, _ := json.Marshal(ep)
	env := map[string]string{
		EnvRecorder:  "1",
		EnvEndpoint:  string(epJSON),
		EnvFps:       strconv.Itoa(fps),
		EnvStateDir:  in.StateDir,
		EnvSessionID: in.SessionId,
	}
	if venue != "" {
		env[EnvVenue] = venue
	}
	if in.Phase != "" {
		env[EnvPhase] = in.Phase
	}
	return sessionRequest{
		Op:        "spawn",
		SessionID: in.SessionId,
		Command:   []string{exe, "__dummy-arg"},
		LogDir:    logDir,
		Env:       env,
	}
}

// sessionStart resolves nothing more (the endpoint came from Invoke) — it submits the
// recorder spawn to the runner service and reports the session as started. state_dir
// is REQUIRED: the recorder (and the runner's handle) land the capture + evidence
// there, and the instrument lifecycle reads row.json back from it.
func sessionStart(ctx context.Context, cc kit.CheckContext, ep *vmEndpoint, in *params.LibvirtVerbInput, venueDefault string) (string, error) {
	if in.StateDir == "" {
		return "", fmt.Errorf("session start: state_dir required")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("session start: resolve own binary: %w", err)
	}
	venue := in.Venue
	if venue == "" {
		venue = venueDefault
	}
	// log_dir: the runner's session handle + the recorder state live in the check run
	// dir. The instrument lifecycle threads the run dir over the wire
	// (buildInstrumentOp's log_dir); a plan-step session (no runner envelope) falls
	// back to "" — the runner's cwd-relative default.
	logDir := in.LogDir
	req := buildSessionSpawn(in, ep, exe, venue, logDir)
	if err := submitSession(ctx, cc, req); err != nil {
		return "", fmt.Errorf("session start: %w", err)
	}
	return fmt.Sprintf("session %s started", in.SessionId), nil
}

// sessionStop signals the runner to finalize the session (SIGTERM to the detached
// recorder), then verifies the recorder's finalize actually landed: the evidence
// row.json must exist in the state dir. Returns a one-line summary of the row.
func sessionStop(ctx context.Context, cc kit.CheckContext, in *params.LibvirtVerbInput) (string, error) {
	if in.StateDir == "" {
		return "", fmt.Errorf("session stop: state_dir required to verify the evidence row")
	}
	// log_dir rides the stop request too: the runner resolves the handle under the
	// run dir the SPAWN used (sessionStateDir(log_dir, id)). A stop without it
	// looks up the cwd-relative default and finds "no session on record" — the
	// spawn-side fix alone moved the handle out of the default's reach.
	req := sessionRequest{Op: "stop", SessionID: in.SessionId, LogDir: in.LogDir}
	if err := submitSession(ctx, cc, req); err != nil {
		return "", fmt.Errorf("session stop: %w", err)
	}
	// The runner's stop is synchronous (SIGTERM → grace → SIGKILL), but the
	// recorder's finalize is its OWN SIGTERM trap: the evidence row lands when the
	// recorder writes it, which can lag the process exit (a blocked display poll
	// delays the trap's finalize). Wait (bounded) for the row instead of reading
	// once — a stop that returns before the row is on disk is a false failure.
	rowPath := filepath.Join(in.StateDir, evidenceFile)
	raw, err := waitForEvidenceRow(ctx, rowPath, sessionStopRowTimeout)
	if err != nil {
		return "", fmt.Errorf("session stop: %w", err)
	}
	var row evidenceRow
	if err := json.Unmarshal(raw, &row); err != nil {
		return "", fmt.Errorf("session stop: decode evidence row %q: %w", rowPath, err)
	}
	return fmt.Sprintf("session %s stopped · evidence: instrument=%s origin=%s artifact=%s (%s)",
		in.SessionId, row.Instrument, row.Origin, rowArtifact(row), filepath.Join(in.StateDir, finalMarker)), nil
}

// sessionStopRowTimeout bounds the stop's wait for the recorder's evidence row.
// The runner's stop ladder (SIGTERM → grace → SIGKILL) already bounds the process
// exit; this bounds the row's LANDING, which can lag the exit (the recorder's
// finalize is its own SIGTERM trap — an in-flight libvirt framebuffer poll
// delays it). 10s is generous for a two-file write and short enough that a crashed
// recorder (no row, ever) fails the stop fast.
const sessionStopRowTimeout = 10 * time.Second

// waitForEvidenceRow polls for the recorder's evidence row with a bounded deadline:
// the row appears when the recorder's SIGTERM trap finalizes, which can lag the
// runner's stop return. A missing row at the deadline means the recorder never
// finalized (crashed before its trap, or killed without one) — a real failure,
// reported with the path so the operator can inspect the state dir.
func waitForEvidenceRow(ctx context.Context, rowPath string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for {
		raw, err := os.ReadFile(rowPath)
		if err == nil {
			return raw, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("%q: %w", rowPath, err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%q: evidence row missing after %s (recorder never finalized)", rowPath, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// rowArtifact renders the row's first artifact as "path[kind]" (a stop path always
// carries the frames.mjpeg artifact the recorder wrote).
func rowArtifact(row evidenceRow) string {
	if len(row.Artifact) == 0 {
		return "<none>"
	}
	a := row.Artifact[0]
	return fmt.Sprintf("%s[%s]", a.Path, a.Kind)
}

// sessionStatus asks the runner for the session handle's liveness and reports it.
func sessionStatus(ctx context.Context, cc kit.CheckContext, in *params.LibvirtVerbInput) (string, error) {
	// log_dir rides the status request too — the runner resolves the handle under
	// the run dir the spawn used (same contract as stop).
	req := sessionRequest{Op: "status", SessionID: in.SessionId, LogDir: in.LogDir}
	raw, err := submitSessionReply(ctx, cc, req)
	if err != nil {
		return "", fmt.Errorf("session status: %w", err)
	}
	var rep sessionStatusReply
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &rep)
	}
	return fmt.Sprintf("session %s alive=%v", in.SessionId, rep.Alive), nil
}

// submitSession sends a session-service request; success is the empty reply.
func submitSession(ctx context.Context, cc kit.CheckContext, req sessionRequest) error {
	_, err := submitSessionReply(ctx, cc, req)
	return err
}

// submitSessionReply sends the request over the EXISTING InvokeProvider reverse leg
// (ClassVerb "session", OpExecute). The runner (plugin-check, compiled-in) dispatches
// it; failures ride the returned error; a status op carries the result JSON back.
// sessionInvokeArgs builds the EXACT reverse-leg invocation the runner's verb:session
// seam decodes (session_seam.go in plugin-check): class "verb", word "session", op
// OpExecute, and the request JSON in the seam's wire shape. PURE + unit-tested so a
// deletion of or drift in the submission path fails a test that would otherwise be
// silent (the runner seam is installed by the Cutover A plugin-check PR).
func sessionInvokeArgs(req sessionRequest) (string, string, []byte, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("session: encode request: %w", err)
	}
	return "verb", "session", reqJSON, nil
}

func submitSessionReply(ctx context.Context, cc kit.CheckContext, req sessionRequest) ([]byte, error) {
	class, word, reqJSON, err := sessionInvokeArgs(req)
	if err != nil {
		return nil, err
	}
	return cc.InvokeProvider(ctx, class, word, ops.OpExecute, reqJSON, nil)
}
