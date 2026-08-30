package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A DEPLOY must not share one answers volume across domains — that is one SSH key for every
// guest, the same reason the cloud_image path re-renders per domain and says so in its own
// comment.
//
// The build persists the rendered answers beside seed.iso so `vm create` can re-pack a
// per-domain copy without re-resolving the distro (the answer FORMAT lives on the distro,
// and create has none resolved). Only authorized_keys is per-domain; hostname, credentials,
// disk and partition geometry describe the INSTALL and belong to the entity, so they must
// survive the round trip byte-for-byte.
func TestRepackPerDomainSeed_SwapsOnlyTheKey(t *testing.T) {
	out := t.TempDir()
	files := map[string]string{
		"user_configuration.json": `{"hostname":"omarchy","disk":"/dev/vda"}`,
		"user_credentials.json":   `{"users":[{"username":"user"}]}`,
		"authorized_keys":         "ssh-ed25519 ENTITYKEY entity@build\n",
	}
	if err := writeSeedSidecar(out, "cidata", files); err != nil {
		t.Fatalf("writeSeedSidecar: %v", err)
	}

	gotID, gotFiles, ok := ReadSeedSidecar(out)
	if !ok {
		t.Fatal("the sidecar did not read back")
	}
	if gotID != "cidata" {
		t.Fatalf("volume id\n got: %q\nwant: cidata", gotID)
	}
	if gotFiles["user_configuration.json"] != files["user_configuration.json"] {
		t.Fatalf("an answer changed in the sidecar round trip: %q", gotFiles["user_configuration.json"])
	}

	dest := filepath.Join(t.TempDir(), "seed.iso")
	const domainKey = "ssh-ed25519 DOMAINKEY charly@domain"
	if err := RepackPerDomainSeed(out, dest, domainKey); err != nil {
		t.Fatalf("RepackPerDomainSeed: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("no per-domain volume written: %v", err)
	}

	blob, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading the per-domain volume: %v", err)
	}
	body := string(blob)
	if !strings.Contains(body, domainKey) {
		t.Error("the per-domain volume does not carry this domain's key")
	}
	if strings.Contains(body, "ENTITYKEY") {
		t.Error("the per-domain volume still carries the ENTITY's key — every guest would share one key")
	}
	// Every other answer byte-identical: they describe the install, not the domain.
	if !strings.Contains(body, `"hostname":"omarchy"`) {
		t.Error("user_configuration.json did not survive the re-pack")
	}
	if !strings.Contains(body, `"username":"user"`) {
		t.Error("user_credentials.json did not survive the re-pack")
	}
}

// An empty pubkey must LEAVE the build's authorized_keys in place, not remove it. On a
// distro that treats the file's presence as "enable sshd and open the firewall", dropping it
// silently produces an unreachable guest — worse than a stale key.
func TestRepackPerDomainSeed_EmptyKeyDoesNotDropTheFile(t *testing.T) {
	out := t.TempDir()
	files := map[string]string{
		"user_configuration.json": `{"hostname":"omarchy"}`,
		"authorized_keys":         "ssh-ed25519 ENTITYKEY entity@build\n",
	}
	if err := writeSeedSidecar(out, "cidata", files); err != nil {
		t.Fatalf("writeSeedSidecar: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "seed.iso")
	if err := RepackPerDomainSeed(out, dest, ""); err != nil {
		t.Fatalf("RepackPerDomainSeed: %v", err)
	}
	blob, _ := os.ReadFile(dest)
	if !strings.Contains(string(blob), "ENTITYKEY") {
		t.Error("an empty pubkey dropped authorized_keys — that leaves sshd disabled and the guest unreachable")
	}
}

// Re-packing without a prior build is an error naming the fix, not a silent empty volume.
func TestRepackPerDomainSeed_WithoutABuildIsAnError(t *testing.T) {
	err := RepackPerDomainSeed(t.TempDir(), filepath.Join(t.TempDir(), "seed.iso"), "k")
	if err == nil {
		t.Fatal("re-packing with no rendered answers must be an error")
	}
	if !strings.Contains(err.Error(), "vm build") {
		t.Fatalf("the error must name the fix; got: %v", err)
	}
}

// The sidecar is 0600. An ENCRYPTED install's answers carry the LUKS passphrase in
// plaintext, because the installer needs it to create the volume; the volume itself is
// world-readable once attached to a guest, but this host-side copy need not be.
func TestWriteSeedSidecar_IsNotWorldReadable(t *testing.T) {
	out := t.TempDir()
	if err := writeSeedSidecar(out, "cidata", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("writeSeedSidecar: %v", err)
	}
	st, err := os.Stat(seedSidecarPath(out))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Fatalf("sidecar mode\n got: %04o\nwant: 0600 (it can hold a LUKS passphrase)", mode)
	}
}
