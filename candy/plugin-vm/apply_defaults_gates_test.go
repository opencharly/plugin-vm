package vm

import (
	"context"
	"testing"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/loaderkit"
	"github.com/opencharly/spec/spec"
)

// vmGateSource is the #Vm a stub host SERVES. The base contract's declaration for this kind is the
// resolved ENVELOPE (#ResolvedVm, `firmware!: string`), so this stub's set is deliberately NARROWER
// than the envelope's: a body the envelope admits is refused when the host's declaration is the one
// applied, which is the observable difference that proves the sourced gate reached the fill.
const vmGateSource = "package schema\n#Vm: {firmware!: \"bios\" | \"uefi-insecure\"}"

// probeVmBody stands in for the resolved vm body the production fill receives. Only the field the
// probe gate constrains is named — the fill's business with the real body is the SDK's, exercised
// by the charly tests that own it; what THIS suite must pin is that the gates the host shipped
// are the ones reaching the fill.
type probeVmBody struct {
	Firmware string `yaml:"firmware" json:"firmware"`
}

// TestApplyCueDefaultsResolvesAVmGate pins the MECHANISM the plugin relies on: the vm body lives
// in plugin-substrate, so a kind whose def is carried by a threaded gate resolves while the same
// kind fails without a gate. The negative control is the point — the gate argument is proven to be
// USED, not merely accepted.
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
}

// withHostGates points the plugin at a host that answers the given snapshot, for one test. The
// reverse channel needs a LIVE host, so this is the only way the seam's POSITIVE branch can be
// exercised here — and it exercises the real vmKindGates/applyVmDefaults above the seam, not a
// re-implementation of them.
func withHostGates(t *testing.T, gates map[string]spec.KindGate) {
	t.Helper()
	savedAsk, savedCtx, savedExec := kindGatesFromHost, cmdCtx, cmdExec
	t.Cleanup(func() { kindGatesFromHost, cmdCtx, cmdExec = savedAsk, savedCtx, savedExec })
	kindGatesFromHost = func(context.Context, *sdk.Executor) map[string]spec.KindGate { return gates }
	cmdCtx, cmdExec = context.Background(), &sdk.Executor{}
}

// TestVmKindGatesSourcesTheHostSnapshot pins BOTH branches of the seam. Absence: with no executor
// there is no host to ask, so it answers nil and the fill falls back to the base contract rather
// than inventing gates. POSITIVE: a connected host's snapshot is returned unchanged — so a seam
// rewritten to `return nil` unconditionally fails here, which is exactly the regression this PR
// exists to guard.
func TestVmKindGatesSourcesTheHostSnapshot(t *testing.T) {
	savedCtx, savedExec := cmdCtx, cmdExec
	t.Cleanup(func() { cmdCtx, cmdExec = savedCtx, savedExec })

	// No reverse channel: the seam must say so rather than invent gates.
	cmdExec = nil
	if g := vmKindGates(); g != nil {
		t.Fatalf("with no executor the seam returned %v, want nil (no host to ask)", g)
	}

	// A connected host: the gates are the HOST's, passed through unchanged.
	hostGates := map[string]spec.KindGate{"vm": {Def: "#Vm", Source: vmGateSource}}
	withHostGates(t, hostGates)
	got := vmKindGates()
	if len(got) != 1 || got["vm"].Def != "#Vm" || got["vm"].Source != vmGateSource {
		t.Fatalf("the seam did not pass the host's snapshot through: got %v, want %v", got, hostGates)
	}
}

// TestApplyVmDefaultsFillsFromTheSourcedGates drives the CHANGED path — applyVmDefaults is the
// production fill hostConfigResolve performs — and it is failable in the direction that matters:
// a body carrying a firmware the BASE admits (its #Vm requires a bare string) is REFUSED once the
// fill is handed the host-served #Vm, which only the sourced gate can do. A seam that stopped
// sourcing hands the fill nil, the base admits the body, and this test fails.
func TestApplyVmDefaultsFillsFromTheSourcedGates(t *testing.T) {
	savedCtx, savedExec := cmdCtx, cmdExec
	t.Cleanup(func() { cmdCtx, cmdExec = savedCtx, savedExec })

	// The discriminating direction: the served #Vm's own set is what the fill applied.
	withHostGates(t, map[string]spec.KindGate{"vm": {Def: "#Vm", Source: vmGateSource}})
	if err := applyVmDefaults(&probeVmBody{Firmware: "uefi-secure"}); err == nil {
		t.Fatal("a value outside the host-served #Vm was admitted — the fill did not use the sourced gates")
	}

	// The same body, same production call, NO host: the base is the authority, so it resolves.
	// That is what proves the refusal above came from the GATE rather than from the body.
	cmdCtx, cmdExec = context.Background(), nil
	if err := applyVmDefaults(&probeVmBody{Firmware: "uefi-secure"}); err != nil {
		t.Fatalf("the base stopped resolving a vm body with no reverse channel: %v", err)
	}

	// And a value the served gate DOES admit resolves through it, unchanged.
	withHostGates(t, map[string]spec.KindGate{"vm": {Def: "#Vm", Source: vmGateSource}})
	inside := &probeVmBody{Firmware: "uefi-insecure"}
	if err := applyVmDefaults(inside); err != nil {
		t.Fatalf("applyVmDefaults refused a value the host-served #Vm admits: %v", err)
	}
	if inside.Firmware != "uefi-insecure" {
		t.Fatalf("the body did not survive the served gate's own schema: firmware=%q", inside.Firmware)
	}
}
