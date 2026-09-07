// Command serve is the OUT-OF-PROCESS entrypoint for the vm command plugin: dual-mode
// sdk.Main (serve OR CLI). charly fork/execs this binary in CLI mode for command:vm
// dispatch when the plugin is NOT compiled-in (→ CliMain); the serve half backs the
// out-of-process provider placement. The SAME NewProvider()/NewMeta() compile INTO
// charly in-process when listed in compiled_plugins — placement is invisible.
//
// HIDDEN RECORDER MODE (plan Cutover E, E-2): with CHARLY_LIBVIRT_RECORDER=1 the SAME
// binary skips serving and becomes the DETACHED host-side session recorder — the
// runner's generic background-session service spawns it for a libvirt: session
// start. It dials the libvirt endpoint from env, polls the VM framebuffer at fps into
// $CHARLY_LIBVIRT_STATE_DIR/frames.mjpeg, and on SIGTERM/SIGINT finalizes (FINAL
// marker + evidence row.json) before exiting 0: the runner's stop is complete only
// when row.json is on disk.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	vm "github.com/opencharly/plugin-vm/candy/plugin-vm"
	"github.com/opencharly/sdk"
)

func main() {
	if os.Getenv(vm.EnvRecorder) == "1" {
		os.Exit(recorderMain())
	}
	sdk.Main(vm.NewProvider(), vm.NewMeta(), vm.CliMain)
}

// recorderMain is the detached recorder process entrypoint (see the package doc). It
// returns the process exit code.
func recorderMain() int {
	endpointJSON := os.Getenv(vm.EnvEndpoint)
	stateDir := os.Getenv(vm.EnvStateDir)
	sessionID := os.Getenv(vm.EnvSessionID)
	if endpointJSON == "" || stateDir == "" || sessionID == "" {
		fmt.Fprintf(os.Stderr, "charly-vm recorder: missing env (endpoint=%q state_dir=%q session_id=%q)\n", endpointJSON, stateDir, sessionID)
		return 2
	}
	ep, err := vm.ParseEndpoint([]byte(endpointJSON))
	if err != nil {
		fmt.Fprintf(os.Stderr, "charly-vm recorder: %v\n", err)
		return 2
	}
	fps, _ := strconv.Atoi(os.Getenv(vm.EnvFps))
	cfg := vm.RecorderConfig{
		Endpoint:  ep,
		Fps:       fps,
		StateDir:  stateDir,
		SessionID: sessionID,
		Venue:     os.Getenv(vm.EnvVenue),
		Phase:     os.Getenv(vm.EnvPhase),
	}

	done := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		close(done) // deterministic finalize: FINAL marker + row.json
	}()

	count, err := vm.RunSessionRecorder(cfg, done)
	if err != nil {
		fmt.Fprintf(os.Stderr, "charly-vm recorder: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "charly-vm recorder: finalized session %s frames=%d state_dir=%s\n", sessionID, count, stateDir)
	return 0
}
