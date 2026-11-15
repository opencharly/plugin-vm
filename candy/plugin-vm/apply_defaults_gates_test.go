package vm

import (
	"testing"

	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// TestApplyCueDefaultsResolvesAVmGate pins the call this plugin's hostConfigResolve makes: the
// vm body lives in plugin-substrate now, so the defaults fill resolves through the THREADED
// gates — the base contract alone cannot. The negative control is the point: without the gate
// the same call fails, so the argument is proven to be used rather than merely passed.
func TestApplyCueDefaultsResolvesAVmGate(t *testing.T) {
	const src = "package schema\n#VmGateProbe: {firmware!: *\"bios\" | string}"

	// The negative control first: with no gates threaded, the base has no such def and the call
	// must fail. If this ever passes, the gate argument is being ignored.
	var out struct {
		Firmware string `yaml:"firmware" json:"firmware"`
	}
	if err := loaderkit.ApplyCueDefaults("vm-gate-probe", &out, nil); err == nil {
		t.Fatal("an undeclared kind resolved without a gate — the threaded KindGates are ignored")
	}

	gates := spec.Threaded{KindGates: map[string]spec.KindGate{
		"vm-gate-probe": {Def: "#VmGateProbe", Source: src},
	}}
	if err := loaderkit.ApplyCueDefaults("vm-gate-probe", &out, gates.KindGates); err != nil {
		t.Fatalf("the declared gate did not resolve: %v", err)
	}
	// The gate RESOLVED — that is what this test asserts: the def came from the served source,
	// not the base. (Whether the def's default materializes into `out` is the fill's own
	// contract, exercised by the charly tests that own it; asserting it here would test the
	// decoder, not the gate.)
}
