package vm

import (
	"encoding/json"
	"errors"
	"testing"

	pb "github.com/opencharly/spec/proto"
)

// guest_ping_test.go — coverage for the `guest-ping` readiness verdict. This is the
// primitive the VM deploy's prepare-venue gate consumes to hard-fail a terminal domain
// instead of burning its readiness cap; it must be exercised, not just declared.

// TestGuestPingState covers the pure verdict derivation for every domain state the deploy
// must distinguish: running+agent-up is the ONLY ready case; a running domain whose agent
// has not connected yet is TRANSIENT (not failed); every dead/absent/unreachable state is
// TERMINAL (failed).
func TestGuestPingState(t *testing.T) {
	cases := []struct {
		name       string
		state      string
		probeErr   error
		wantReady  bool
		wantFailed bool
	}{
		{"running + agent answers", "running", nil, true, false},
		{"running + agent not yet up (transient)", "running", errors.New("agent not connected"), false, false},
		{"crashed domain (terminal)", "crashed", nil, false, true},
		{"shut off domain (terminal)", "shut off", nil, false, true},
		{"absent domain (terminal)", "absent", nil, false, true},
		{"unreachable libvirt (terminal)", "unreachable", errors.New("connect refused"), false, true},
		{"paused domain (not ready, not terminal)", "paused", nil, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := guestPingState(c.state, c.probeErr)
			if got.Ready != c.wantReady {
				t.Errorf("Ready = %v, want %v", got.Ready, c.wantReady)
			}
			if got.Failed != c.wantFailed {
				t.Errorf("Failed = %v, want %v", got.Failed, c.wantFailed)
			}
			if got.State != c.state {
				t.Errorf("State = %q, want %q", got.State, c.state)
			}
			if c.probeErr != nil && got.Error == "" {
				t.Errorf("Error must carry the probe failure %q", c.probeErr)
			}
			if c.probeErr == nil && got.Error != "" {
				t.Errorf("Error must be empty on a clean probe, got %q", got.Error)
			}
		})
	}
}

// TestGuestPingState_EmptyStateIsAbsent pins the "" -> absent normalization (the caller
// passes "" when the domain could not be observed at all).
func TestGuestPingState_EmptyStateIsAbsent(t *testing.T) {
	got := guestPingState("", nil)
	if got.State != "absent" || !got.Failed || got.Ready {
		t.Fatalf("empty state must normalize to a terminal absent verdict, got %+v", got)
	}
}

// TestInvokeGuestPing_UnreachableIsTerminal proves the Invoke dispatch reaches the guest-ping
// branch and returns a TERMINAL verdict when libvirt cannot be reached — exercising the
// op wiring end-to-end without a live domain.
func TestInvokeGuestPing_UnreachableIsTerminal(t *testing.T) {
	env, _ := json.Marshal(vmEnv{VmOp: "guest-ping", VmName: "charly-does-not-exist", URI: "qemu+tcp://127.0.0.1:1/system"})
	reply, err := vmProvider{}.Invoke(t.Context(), &pb.InvokeRequest{Op: "run", Reserved: "libvirt", EnvJson: env})
	if err != nil {
		t.Fatalf("Invoke(guest-ping) error = %v", err)
	}
	var got guestPingReply
	if uerr := json.Unmarshal(reply.GetResultJson(), &got); uerr != nil {
		t.Fatalf("decode reply: %v", uerr)
	}
	if got.Ready {
		t.Fatalf("an unreachable libvirt must not be ready, got %+v", got)
	}
	if !got.Failed {
		t.Fatalf("an unreachable libvirt must be a TERMINAL verdict, got %+v", got)
	}
}
