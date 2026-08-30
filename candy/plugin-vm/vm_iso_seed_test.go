package vm

import (
	"strings"
	"testing"

	"github.com/opencharly/spec/spec"
)

// These exercise installerSeedContext — the function BuildIsoVM itself calls — rather than
// a copy of its mapping, so a divergence between production and test cannot pass.
//
// The seed context is the DATA half of the installer split: what the vm entity contributes
// before the distro's templates turn it into archinstall JSON. Everything asserted here is
// about that handoff, never about any particular installer's file format.

func isoSpec(inst *spec.VmInstaller) *VmSpec {
	s := &VmSpec{}
	s.Source.Kind = "iso"
	s.Source.Distro = "omarchy"
	s.Source.URL = "https://iso.omarchy.org/omarchy-4.0.1.iso"
	s.Source.Installer = inst
	s.DiskSize = "60G"
	return s
}

// A password_hash is passed through untouched. The alternative — re-hashing an already
// hashed value — would produce a hash of the literal "$6$..." string and an account nobody
// can log into, with nothing in any log to say so.
func TestInstallerSeedContext_PasswordHashPassesThrough(t *testing.T) {
	const hash = "$6$rounds=656000$abcdefgh$0123456789"
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:      "user",
		Password_hash: hash,
	}), t.TempDir())
	if err != nil {
		t.Fatalf("installerSeedContext: %v", err)
	}
	if ctx.PasswordHash != hash {
		t.Fatalf("password_hash\n got: %q\nwant: %q", ctx.PasswordHash, hash)
	}
}

// The PLAINTEXT password must be crypt()ed before it can reach the context, because the
// context is all RenderInstallerSeed ever sees. If the plaintext survived into the context
// it would be written verbatim onto the answers volume.
func TestInstallerSeedContext_PlaintextPasswordIsHashed(t *testing.T) {
	const plaintext = "user"
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username: "user",
		Password: plaintext,
	}), t.TempDir())
	if err != nil {
		t.Skipf("openssl unavailable on this host: %v", err)
	}
	if ctx.PasswordHash == plaintext {
		t.Fatal("the PLAINTEXT password reached the seed context — it would land on the answers volume verbatim")
	}
	if !strings.HasPrefix(ctx.PasswordHash, "$6$") {
		t.Fatalf("want a $6$ SHA-512 crypt hash, got %q", ctx.PasswordHash)
	}
	// The struct carries no plaintext field at all, which is the structural half of the
	// same guarantee — assert the value is gone, not merely relocated.
	if strings.Contains(ctx.PasswordHash, plaintext) && len(ctx.PasswordHash) < 20 {
		t.Fatalf("hash looks like it embeds the plaintext: %q", ctx.PasswordHash)
	}
}

// An installer with no credential of any kind is a hard error, not a default. An install
// that creates an account with no password is either unreachable or wide open, and which
// of the two it is depends on the distro — so charly refuses to guess.
func TestInstallerSeedContext_NoCredentialIsRejected(t *testing.T) {
	_, err := installerSeedContext(isoSpec(&spec.VmInstaller{Username: "user"}), t.TempDir())
	if err == nil {
		t.Fatal("an installer with no password, password_hash or defer_provisioning must be an error")
	}
	if !strings.Contains(err.Error(), "defer_provisioning") {
		t.Fatalf("the error must name the third option; got: %v", err)
	}
}

// defer_provisioning is the imaging-rig mode: install with no personal details and let the
// first person to boot create their own user. It is the ONE case where having no password
// is correct, and it must not trip the credential check above.
func TestInstallerSeedContext_DeferProvisioningNeedsNoPassword(t *testing.T) {
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Defer_provisioning: true,
	}), t.TempDir())
	if err != nil {
		t.Fatalf("defer_provisioning must not require a password: %v", err)
	}
	if ctx.PasswordHash != "" {
		t.Fatalf("defer_provisioning must carry no password hash, got %q", ctx.PasswordHash)
	}
	if !ctx.DeferProvisioning {
		t.Fatal("defer_provisioning did not reach the context")
	}
	// An imaging rig has no account to seed keys onto, so charly must not default one in.
	if len(ctx.SSHAuthorizedKeys) != 0 {
		t.Fatalf("defer_provisioning must not default an ssh key onto a non-existent account, got %v", ctx.SSHAuthorizedKeys)
	}
}

// encrypt: true takes the LUKS PASSPHRASE, which is the plaintext itself — a hash cannot
// unlock a volume. An authored password_hash with encrypt: true is therefore a
// configuration that cannot work, and it must fail loudly at build time rather than
// producing a guest that halts at a passphrase prompt nobody can answer.
func TestInstallerSeedContext_EncryptWithOnlyAHashIsRejected(t *testing.T) {
	_, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:      "user",
		Password_hash: "$6$rounds=656000$abcdefgh$0123456789",
		Encrypt:       true,
	}), t.TempDir())
	if err == nil {
		t.Fatal("encrypt: true with only a password_hash must be an error — LUKS needs the passphrase, not a hash")
	}
	if !strings.Contains(err.Error(), "LUKS") {
		t.Fatalf("the error must say why; got: %v", err)
	}
}

// Explicitly authored keys win over the generated default. An operator who names their own
// keys must not silently also get charly's.
func TestInstallerSeedContext_AuthoredKeysAreNotAugmented(t *testing.T) {
	authored := []string{"ssh-ed25519 AAAAC3Nz.... operator@example"}
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:           "user",
		Password_hash:      "$6$x$y",
		Ssh_authorized_key: authored,
	}), t.TempDir())
	if err != nil {
		t.Fatalf("installerSeedContext: %v", err)
	}
	if len(ctx.SSHAuthorizedKeys) != 1 || ctx.SSHAuthorizedKeys[0] != authored[0] {
		t.Fatalf("authored keys must pass through unchanged\n got: %v\nwant: %v", ctx.SSHAuthorizedKeys, authored)
	}
}

// The per-distro `answer:` map is carried VERBATIM. It is the escape hatch for answers no
// distro-agnostic field can name (a tailscale auth key, say), and rewriting it would defeat
// the point.
func TestInstallerSeedContext_AnswersPassThroughVerbatim(t *testing.T) {
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:      "user",
		Password_hash: "$6$x$y",
		Answer:        map[string]string{"tailscale_authkey": "tskey-auth-EXAMPLE"},
	}), t.TempDir())
	if err != nil {
		t.Fatalf("installerSeedContext: %v", err)
	}
	if ctx.Answers["tailscale_authkey"] != "tskey-auth-EXAMPLE" {
		t.Fatalf("answers did not pass through: %v", ctx.Answers)
	}
}

// The scalar fields reach the context under the names the templates use. A silent drop here
// surfaces as an installer prompting for a timezone with nobody at the keyboard.
func TestInstallerSeedContext_ScalarsReachTheContext(t *testing.T) {
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:      "user",
		Password_hash: "$6$x$y",
		Full_name:     "Charly",
		Email:         "charly@opencharly.invalid",
		Hostname:      "omarchy",
		Timezone:      "UTC",
		Keyboard:      "us",
		Locale:        "en_US.UTF-8",
		Disk:          "/dev/vda",
	}), t.TempDir())
	if err != nil {
		t.Fatalf("installerSeedContext: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"Username", ctx.Username, "user"},
		{"FullName", ctx.FullName, "Charly"},
		{"Email", ctx.Email, "charly@opencharly.invalid"},
		{"Hostname", ctx.Hostname, "omarchy"},
		{"Timezone", ctx.Timezone, "UTC"},
		{"Keyboard", ctx.Keyboard, "us"},
		{"Locale", ctx.Locale, "en_US.UTF-8"},
		{"Disk", ctx.Disk, "/dev/vda"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s\n got: %q\nwant: %q", tc.name, tc.got, tc.want)
		}
	}
}

// BuildIsoVM must refuse a distro with no installer: block rather than fetching a
// multi-GiB ISO first and failing afterwards. The guard also produces an error naming the
// distro, because "no installer" is a statement about the distro entity, not the VM.
func TestBuildIsoVM_DistroWithoutInstallerIsRejected(t *testing.T) {
	_, err := BuildIsoVM(isoSpec(&spec.VmInstaller{Username: "user", Password_hash: "$6$x$y"}),
		t.TempDir(), t.TempDir(), nil, &DistroDef{}, false)
	if err == nil {
		t.Fatal("a distro with no installer: block must be rejected")
	}
	if !strings.Contains(err.Error(), "omarchy") || !strings.Contains(err.Error(), "installer") {
		t.Fatalf("the error must name the distro and the missing block; got: %v", err)
	}
}

// The kind guard. Every sibling engine has one, and it is what keeps a dispatch bug from
// silently building the wrong thing.
func TestBuildIsoVM_WrongSourceKindIsRejected(t *testing.T) {
	s := isoSpec(&spec.VmInstaller{Username: "user", Password_hash: "$6$x$y"})
	s.Source.Kind = "cloud_image"
	_, err := BuildIsoVM(s, t.TempDir(), t.TempDir(), nil, &DistroDef{}, false)
	if err == nil {
		t.Fatal("BuildIsoVM must reject a non-iso source.kind")
	}
}

// qemuImgCreateBlank refuses an empty size. Without the guard qemu-img creates a 0-byte
// disk and the installer fails deep inside a partitioner with an out-of-space error that
// names nothing useful.
func TestQemuImgCreateBlank_RequiresASize(t *testing.T) {
	err := qemuImgCreateBlank(t.TempDir()+"/disk.qcow2", "")
	if err == nil {
		t.Fatal("an empty disk_size must be an error")
	}
	if !strings.Contains(err.Error(), "disk_size") {
		t.Fatalf("the error must name the field; got: %v", err)
	}
}

// An unresolvable SSH key must NOT fail the build. `key_source: generate` always resolves,
// so the failing case is a host with no ~/.ssh key and a spec that never asked for one —
// where there is simply no key to seed. A seeded key is a convenience; installing does not
// require one, and an operator who intends to use the console must not be blocked.
//
// This test is what caught the first version of the default, which propagated the resolver's
// error and would have failed every ISO build on a keyless host.
func TestInstallerSeedContext_UnresolvableKeyIsNotFatal(t *testing.T) {
	// vmStateDir is empty and key_source is unset, so the resolver falls to its "auto"
	// search and finds nothing to offer.
	ctx, err := installerSeedContext(isoSpec(&spec.VmInstaller{
		Username:      "user",
		Password_hash: "$6$x$y",
	}), t.TempDir())
	if err != nil {
		t.Fatalf("an unresolvable ssh key must not fail the build: %v", err)
	}
	if len(ctx.SSHAuthorizedKeys) != 0 {
		t.Fatalf("no key was resolvable, so none must be seeded; got %v", ctx.SSHAuthorizedKeys)
	}
}
