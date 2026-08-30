package vm

import (
	"os"
	"strings"
	"testing"

	"github.com/opencharly/sdk/kit"
)

// The managed ssh alias is what EVERY consumer uses — the bed runner's readiness gate,
// plugin-deploy-vm's prepare-venue, and `charly vm ssh` alike. So the host-key policy for an
// installer-driven guest has to be right HERE, once.
//
// An iso guest changes its SSH host identity exactly once: the live installer environment's
// sshd is pinned under accept-new by the first thing that connects, then the guest reboots
// into the installed system with different keys and every later connection fails
// "Host key ... has changed" — which polling cannot recover from.
//
// Measured on a real guest: with the stale line present the alias returns "Host key
// verification failed"; with it removed the same alias reports hostname=omarchy,
// root=btrfs, sshd=enabled.
func TestVmSshStanza_IsoGuestRecordsNoHostKey(t *testing.T) {
	iso := &VmSpec{}
	iso.Source.Kind = "iso"

	stanza := renderStanzaFor(t, iso, "check-omarchy-iso-vm")

	if !strings.Contains(stanza, "UserKnownHostsFile "+os.DevNull) {
		t.Errorf("an iso alias must not record a host key; got:\n%s", stanza)
	}
	// accept-new still applies: the first connection is accepted exactly as before. What is
	// given up is only the RECORDING, so nothing can later conflict.
	if !strings.Contains(stanza, "StrictHostKeyChecking accept-new") {
		t.Errorf("the alias must keep accept-new; got:\n%s", stanza)
	}
}

// Every OTHER source kind boots ONE system with ONE identity and keeps a real per-domain
// known_hosts. Relaxing them would weaken host-key continuity for every existing VM in
// exchange for nothing.
func TestVmSshStanza_OtherSourceKindsKeepTheirKnownHosts(t *testing.T) {
	for _, kind := range []string{"cloud_image", "bootc", "bootstrap", ""} {
		vm := &VmSpec{}
		vm.Source.Kind = kind

		stanza := renderStanzaFor(t, vm, "check-other-vm")

		if strings.Contains(stanza, "UserKnownHostsFile "+os.DevNull) {
			t.Errorf("kind %q: host-key recording was disabled for a VM with one stable identity; got:\n%s", kind, stanza)
		}
		if !strings.Contains(stanza, "known_hosts") {
			t.Errorf("kind %q: the per-domain known_hosts was dropped; got:\n%s", kind, stanza)
		}
	}
}

// renderStanzaFor publishes the alias against a TEMP home and reads back the stanza it
// wrote — driving publishVmSshAlias itself rather than a copy of its policy, so a
// divergence between production and test cannot pass.
func renderStanzaFor(t *testing.T, vm *VmSpec, domain string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CHARLY_VM_STATE_DIR", t.TempDir())

	if err := publishVmSshAlias(home, domain, vm, VmRuntimeParams{SSHPort: 40563}); err != nil {
		t.Fatalf("publishVmSshAlias: %v", err)
	}
	blob, err := os.ReadFile(kit.SshFragmentPath(home))
	if err != nil {
		t.Fatalf("reading the published ssh fragment: %v", err)
	}
	return string(blob)
}
