package vm

import (
	"encoding/json"
	"errors"
	"testing"

	libvirt "github.com/digitalocean/go-libvirt"
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

// TestDomainStateReply_TerminalField pins the `domain-state` reply's `failed` field for EVERY
// terminal state (including the absent/unreachable arms the pre-review omitted it from), derived
// from the ONE terminalDomainState predicate the guest-ping verdict also uses.
func TestDomainStateReply_TerminalField(t *testing.T) {
	cases := []struct {
		state      string
		wantFailed bool
	}{
		{"running", false},
		{"paused", false},
		{"crashed", true},
		{"shut off", true},
		{"absent", true},
		{"unreachable", true},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			r := makeDomainStateReply(true, c.state == "running", c.state, "")
			if r.Failed != c.wantFailed {
				t.Errorf("domain-state %q: Failed = %v, want %v", c.state, r.Failed, c.wantFailed)
			}
			if r.State != c.state {
				t.Errorf("domain-state %q: State = %q", c.state, r.State)
			}
		})
	}
}

// TestTerminalDomainState_OneDefinition guards the single terminal predicate both ops share.
func TestTerminalDomainState_OneDefinition(t *testing.T) {
	for _, s := range []string{"absent", "unreachable", "crashed", "shut off"} {
		if !terminalDomainState(s) {
			t.Errorf("terminalDomainState(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"running", "paused", "suspended", ""} {
		if terminalDomainState(s) {
			t.Errorf("terminalDomainState(%q) = true, want false", s)
		}
	}
}

// TestInvokeDomainState_UnreachableIsTerminal proves the `domain-state` DISPATCH branch (not
// just the pure helper) reaches the terminal verdict on a live connect failure, so reverting the
// dispatch to the old inline maps (which omitted `failed`) fails this test.
func TestInvokeDomainState_UnreachableIsTerminal(t *testing.T) {
	env, _ := json.Marshal(vmEnv{VmOp: "domain-state", VmName: "charly-does-not-exist", URI: "qemu+tcp://127.0.0.1:1/system"})
	reply, err := vmProvider{}.Invoke(t.Context(), &pb.InvokeRequest{Op: "run", Reserved: "libvirt", EnvJson: env})
	if err != nil {
		t.Fatalf("Invoke(domain-state) error = %v", err)
	}
	var got domainStateReply
	if uerr := json.Unmarshal(reply.GetResultJson(), &got); uerr != nil {
		t.Fatalf("decode reply: %v", uerr)
	}
	if got.Running || got.Exists {
		t.Fatalf("an unreachable libvirt must not report exists/running, got %+v", got)
	}
	if !got.Failed || got.State != "unreachable" {
		t.Fatalf("an unreachable libvirt must be a TERMINAL domain-state verdict (failed, state=unreachable), got %+v", got)
	}
}

// TestTerminalDomainState_MatchesProducerLiterals proves the terminal predicate's string keys are
// exactly what domainStateString PRODUCES for the libvirt states it treats as terminal — so the
// tested literals cannot drift from the producer.
func TestTerminalDomainState_MatchesProducerLiterals(t *testing.T) {
	// The producer emits these strings for the states the predicate must classify terminal.
	for _, st := range []libvirt.DomainState{libvirt.DomainCrashed, libvirt.DomainShutoff} {
		s := domainStateString(st)
		if !terminalDomainState(s) {
			t.Errorf("domainStateString(%v) = %q, which terminalDomainState does NOT classify terminal", st, s)
		}
	}
	// And `running` (the ONLY non-terminal producer literal) is not terminal.
	if terminalDomainState(domainStateString(libvirt.DomainRunning)) {
		t.Errorf("running must not be terminal")
	}
}
