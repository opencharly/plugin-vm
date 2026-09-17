package vm

import (
	"encoding/json"
	"os"
	"testing"

	pb "github.com/opencharly/spec/proto"
)

// guest_ping_live_test.go — the LIVE exercise of the `guest-ping` op's changed branch
// (a RUNNING domain -> NewGuestAgent(...).Ping()). The in-process test
// TestInvokeGuestPing_UnreachableIsTerminal only drives the connect-failure leg; this one
// drives the real libvirt + qemu-guest-agent path against a running domain.
//
// Opt-in and hermetic-by-explicit-target: set
//
//	GUEST_PING_TEST_DOMAIN=charly-<domain>    (a RUNNING libvirt domain with the agent channel)
//	GUEST_PING_TEST_URI=qemu:///session       (optional; defaults to the session URI)
//
// to run it. Skipped otherwise, so `go test ./...` stays portable.
func TestInvokeGuestPing_LiveRunningDomain(t *testing.T) {
	domain := os.Getenv("GUEST_PING_TEST_DOMAIN")
	if domain == "" {
		t.Skip("set GUEST_PING_TEST_DOMAIN (a RUNNING domain with the qemu-guest-agent channel) to run the live op")
	}
	uri := os.Getenv("GUEST_PING_TEST_URI")

	env, _ := json.Marshal(vmEnv{VmOp: "guest-ping", VmName: domain, URI: uri})
	reply, err := vmProvider{}.Invoke(t.Context(), &pb.InvokeRequest{Op: "run", Reserved: "libvirt", EnvJson: env})
	if err != nil {
		t.Fatalf("Invoke(guest-ping) error = %v", err)
	}
	var got guestPingReply
	if uerr := json.Unmarshal(reply.GetResultJson(), &got); uerr != nil {
		t.Fatalf("decode reply: %v", uerr)
	}
	if !got.Ready {
		t.Fatalf("a RUNNING domain with a live guest agent must be ready, got %+v", got)
	}
	if got.State != "running" {
		t.Fatalf("State = %q, want running", got.State)
	}
	if got.Failed {
		t.Fatalf("a ready domain must not be terminal, got %+v", got)
	}
	t.Logf("guest-ping live verdict: %+v", got)

	// The terminal leg against a domain that does NOT exist: a NON-ready, FAILED verdict —
	// the deploy gate's fast-abort path, verified live on the same connection.
	env, _ = json.Marshal(vmEnv{VmOp: "guest-ping", VmName: "charly-definitely-absent-xyz", URI: uri})
	reply, err = vmProvider{}.Invoke(t.Context(), &pb.InvokeRequest{Op: "run", Reserved: "libvirt", EnvJson: env})
	if err != nil {
		t.Fatalf("Invoke(guest-ping absent) error = %v", err)
	}
	var absent guestPingReply
	if uerr := json.Unmarshal(reply.GetResultJson(), &absent); uerr != nil {
		t.Fatalf("decode absent reply: %v", uerr)
	}
	if absent.Ready || !absent.Failed || absent.State != "absent" {
		t.Fatalf("an absent domain must be a TERMINAL verdict, got %+v", absent)
	}
	t.Logf("guest-ping absent verdict: %+v", absent)
}
