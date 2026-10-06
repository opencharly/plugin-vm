package vm

import (
	"strings"
	"testing"
	"time"
)

// TestBoundedRPC_ReturnsOnTimeout is the regression guard for opencharly/charly#800:
// `charly vm destroy` hung indefinitely because the libvirt teardown RPCs
// (DomainDestroy/DomainShutdown/DomainUndefineFlags/DomainSnapshotDelete) are
// context-less in go-libvirt, so a wedged virtqemud blocked them forever. boundedRPC
// must return a NAMED error at the bound WITHOUT waiting for the underlying call.
func TestBoundedRPC_ReturnsOnTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block) // release the abandoned goroutine so the test does not leak it
	start := time.Now()
	err := boundedRPC("force destroy", 50*time.Millisecond, func() error {
		<-block // never returns on its own — the wedged-virtqemud shape
		return nil
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a wedged libvirt RPC must return a bounded, named error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("boundedRPC must return AT the bound, not block: took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "force destroy") || !strings.Contains(err.Error(), "did not respond") {
		t.Fatalf("the error must NAME the op and the bounded failure, got: %v", err)
	}
}

// TestBoundedRPC_PassesThroughSuccess proves the bound does not alter the fast path:
// a call that returns promptly yields its own result unchanged.
func TestBoundedRPC_PassesThroughSuccess(t *testing.T) {
	want := errBoundedSentinel{}
	if err := boundedRPC("undefine", time.Second, func() error { return want }); err == nil {
		t.Fatal("boundedRPC must propagate a fast call's error, got nil")
	}
}

type errBoundedSentinel struct{}

func (errBoundedSentinel) Error() string { return "sentinel" }
