package vm

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// vm_bake_test.go — the layered VM bake (cutover task 6): the pure decisions
// behind the bake command.

func TestSplitCsv(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitCsv(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("splitCsv(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The bake requires a clone source (a layered VM bakes a clone base). This
// test drives the REAL guard (requireCloneSource — the function VmBakeCmd.Run
// calls), so a removed or weakened guard fails the test.
func TestBakeRequiresCloneKind(t *testing.T) {
	nonClone := []string{"cloud_image", "bootc", "bootstrap", "iso", "imported"}
	for _, kind := range nonClone {
		s := &VmSpec{}
		s.Source.Kind = kind
		if err := requireCloneSource(s, "bake-test"); err == nil {
			t.Fatalf("requireCloneSource must refuse source.kind %q, got nil", kind)
		}
	}
	// A clone source passes.
	s := &VmSpec{}
	s.Source.Kind = "clone"
	if err := requireCloneSource(s, "bake-test"); err != nil {
		t.Fatalf("requireCloneSource must accept source.kind clone, got %v", err)
	}
	// A nil spec is refused (the no-entity path).
	if err := requireCloneSource(nil, "bake-test"); err == nil {
		t.Fatal("requireCloneSource must refuse a nil spec")
	}
}

// guestAgentEnableSshArgs is the EXACT ssh subprocess invocation the bake
// runs to enable qemu-guest-agent in the freshly-booted guest (phases 2.5).
// The subprocess itself needs a live guest; the command-construction is what
// this test locks — a changed invocation (alias, flags, command) fails it.
func TestGuestAgentEnableSshArgs(t *testing.T) {
	args := guestAgentEnableSshArgs("charly-clone-vm")
	want := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"charly-clone-vm",
		"sudo", "systemctl", "enable", "--now", "qemu-guest-agent",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("guestAgentEnableSshArgs = %v, want %v", args, want)
	}
}

// bakePollUntil is the bake's bounded-wait primitive (the agent-enable + the
// agent-ping both poll through it). A probe that succeeds must return
// immediately; a probe that keeps failing must time out and surface the last
// error — a removed or un-bounded wait fails these.
func TestBakePollUntil(t *testing.T) {
	calls := 0
	err := bakePollUntil(func() error {
		calls++
		if calls >= 2 {
			return nil
		}
		return fmt.Errorf("not yet")
	}, 2*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("a probe that succeeds on the 2nd call must return nil, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 probe calls, got %d", calls)
	}

	// A probe that never succeeds must time out and surface the last error.
	err = bakePollUntil(func() error { return fmt.Errorf("always failing") }, 50*time.Millisecond, 10*time.Millisecond)
	if err == nil {
		t.Fatal("a never-succeeding probe must time out with an error")
	}
}
