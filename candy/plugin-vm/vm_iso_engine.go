package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/sdk/vmshared"
	"github.com/opencharly/spec/spec"
)

// IsoBuildResult is what BuildIsoVM produces: a blank disk for the installer to write,
// the installer ISO it boots, and the answers volume that makes it unattended.
type IsoBuildResult struct {
	DiskPath        string
	InstallerIsoRef string
	SeedIsoPath     string
	InstallerSHA256 string
	SeedFiles       []string
}

// BuildIsoVM builds a VM from a distro's OFFICIAL INSTALLER ISO, run fully unattended.
//
// It is the fourth sibling of BuildCloudImage / BuildBootcVM / BuildBootstrapVM, and it
// is the SIMPLEST of the four, for one reason that is worth stating because both design
// passes got it wrong: THE INSTALLER NEEDS NO EJECT, NO DETACH AND NO SECOND BOOT.
//
// Upstream's own documented recipe attaches the installer ISO and the answers volume as
// two cdroms and sets the boot order to DISK FIRST. An empty disk is not bootable, so the
// firmware falls through to the ISO on the first boot; once the installer has written the
// disk, the disk boots and the ISO is never reached again. Both cdroms stay attached
// forever and nothing has to notice that the install finished. The "installer loops
// forever re-installing" failure mode is not defended against here because the boot order
// makes it unreachable.
//
// So this function does four things: fetch and verify the ISO, render the answers, write
// them to a labelled volume, and allocate a blank disk. The install itself happens when
// the domain first boots, which is `vm create`'s job, not this one's.
func BuildIsoVM(
	vmSpec *VmSpec,
	outputDir, vmStateDir string,
	existingState *VmDeployState,
	distro *DistroDef,
	force bool,
) (IsoBuildResult, error) {
	if vmSpec.Source.Kind != "iso" {
		return IsoBuildResult{}, fmt.Errorf("BuildIsoVM called with source.kind=%q (expected iso)", vmSpec.Source.Kind)
	}
	if distro == nil {
		return IsoBuildResult{}, fmt.Errorf("iso vm: no distro resolved (source.distro is required for iso sources)")
	}
	if distro.Installer == nil {
		return IsoBuildResult{}, fmt.Errorf("iso vm: distro %q declares no installer: — it cannot be installed unattended", vmSpec.Source.Distro)
	}
	// kernel_args is DECLARED on the iso arm and NOT IMPLEMENTED here, so it is rejected
	// rather than ignored.
	//
	// Appending to an installer's cmdline means rewriting the boot configuration INSIDE a
	// multi-GiB ISO — repacking the image, or extracting its kernel and initrd to boot them
	// directly with -kernel/-initrd, which then diverges from how the medium actually boots.
	// Neither is a small change, and neither is needed by anything shipping today.
	//
	// What is NOT acceptable is accepting the field and silently doing nothing with it. An
	// operator adding console=ttyS0 to debug a stalled install would get exactly the silence
	// they were trying to fix, and would conclude the installer never started. That failure
	// shape has cost this codebase real time twice already (ResolveInherits and
	// resolveDistro each silently dropped fields every gate accepted), so the third one
	// errors instead.
	if vmSpec.Source.KernelArgs != "" {
		return IsoBuildResult{}, fmt.Errorf("iso vm: source.kernel_args is not implemented for installer ISOs "+
			"(appending to the cmdline requires rewriting the boot config inside the ISO). "+
			"It is rejected rather than ignored so it cannot look like it took effect; "+
			"got kernel_args=%q", vmSpec.Source.KernelArgs)
	}

	// --- Step 1: Fetch + verify the installer ISO. ---
	// FetchArtifact, not a local copy of a downloader: resumable, sha256-verified, flocked
	// against a concurrent bed, and content-addressed so N entities sharing one ISO URL
	// download it once. An empty checksum auto-resolves a .sha256 sidecar.
	fetched, err := kit.FetchArtifact(vmSpec.Source, ".iso")
	if err != nil {
		return IsoBuildResult{}, fmt.Errorf("fetch installer iso: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return IsoBuildResult{}, fmt.Errorf("creating output dir: %w", err)
	}
	diskPath := filepath.Join(outputDir, "disk.qcow2")
	seedPath := filepath.Join(outputDir, "seed.iso")

	// --- Step 2: Render the answers volume. ---
	// Rendered BEFORE the disk is touched. A bad seed is a hard error here rather than an
	// installer sitting at a prompt nobody is watching, and rendering first means a
	// failure leaves no half-built disk behind.
	seedCtx, err := installerSeedContext(vmSpec, vmStateDir)
	if err != nil {
		return IsoBuildResult{}, err
	}
	files, err := vmshared.RenderInstallerSeed(distro.Installer, seedCtx)
	if err != nil {
		return IsoBuildResult{}, fmt.Errorf("rendering installer seed for distro %q: %w", vmSpec.Source.Distro, err)
	}

	volumeID := distro.Installer.VolumeID
	if volumeID == "" {
		return IsoBuildResult{}, fmt.Errorf("distro %q: installer.volume_id is required — the installer finds the answers by VOLUME LABEL, so an unlabelled volume is invisible to it", vmSpec.Source.Distro)
	}
	if err := vmshared.WriteLabeledISO(seedPath, volumeID, files); err != nil {
		return IsoBuildResult{}, fmt.Errorf("writing answers volume: %w", err)
	}

	// --- Step 3: Allocate the blank disk the installer writes into. ---
	// Rebuilt only when the signature drifted or --force: a blank disk is cheap to make
	// but ruinous to remake, because after the install it is no longer blank — it is the
	// guest. Recreating it silently would destroy an installed system. The stamp is the
	// same mechanism BuildCloudImage uses to protect a base a live overlay backs onto.
	sig := vmBuildStamp{
		BaseSHA256: fetched.SHA256,
		DiskSize:   vmSpec.DiskSize,
		SourceURL:  vmSpec.Source.URL,
	}
	if !force && diskBaseFresh(outputDir, diskPath, sig) {
		fmt.Fprintf(os.Stderr, "Disk %s is content-fresh (installer sha256=%s) — leaving it alone\n", diskPath, fetched.SHA256)
	} else {
		_ = os.Remove(diskPath)
		if err := qemuImgCreateBlank(diskPath, vmSpec.DiskSize); err != nil {
			return IsoBuildResult{}, err
		}
		// Recorded LAST — a matching stamp then implies a COMPLETE build.
		if err := writeVmBuildStamp(outputDir, sig); err != nil {
			return IsoBuildResult{}, fmt.Errorf("writing build stamp: %w", err)
		}
	}

	return IsoBuildResult{
		DiskPath:        diskPath,
		InstallerIsoRef: fetched.Path,
		SeedIsoPath:     seedPath,
		InstallerSHA256: fetched.SHA256,
		SeedFiles:       vmshared.SeedFileNames(files),
	}, nil
}

// installerSeedContext converts the VM entity's authored `installer:` block into the
// render context the distro's templates consume.
//
// This is the DATA half of the split RenderInstallerSeed's doc comment describes: the vm
// entity says who the user is and which disk to wipe, the distro says what file that
// becomes. Nothing here knows what archinstall is.
func installerSeedContext(vmSpec *VmSpec, vmStateDir string) (spec.InstallerSeedContext, error) {
	inst := vmSpec.Source.Installer
	if inst == nil {
		return spec.InstallerSeedContext{}, fmt.Errorf("iso vm: source.installer is required — without it the install is not unattended and would sit at the installer's first prompt")
	}

	// The DISK SIZE, in bytes, so the distro's template can compute a root partition.
	// A real answer format states partition geometry as absolute numbers — archinstall
	// indexes the partition's size key with no default and has no fill-remaining
	// sentinel — and a seed rendered on the host cannot lsblk a disk that does not exist
	// yet.
	//
	// UNCONDITIONAL, not "when set": the template arithmetic renders a NEGATIVE size from
	// a zero value rather than failing, so a missing number would reach the installer as a
	// plausible-looking wrong one. Parsed with parseDiskSizeBytes, the same parser the
	// bootstrap path uses for the same string (R3).
	diskBytes, derr := parseDiskSizeBytes(vmSpec.DiskSize)
	if derr != nil {
		return spec.InstallerSeedContext{}, fmt.Errorf("iso vm: parsing disk_size for the installer seed: %w", derr)
	}

	ctx := spec.InstallerSeedContext{
		DiskSizeBytes:     diskBytes,
		Hostname:          inst.Hostname,
		Timezone:          inst.Timezone,
		Locale:            inst.Locale,
		Keyboard:          inst.Keyboard,
		Username:          inst.Username,
		FullName:          inst.Full_name,
		Email:             inst.Email,
		Disk:              inst.Disk,
		Encrypt:           inst.Encrypt,
		DeferProvisioning: inst.Defer_provisioning,
		Answers:           inst.Answer,
	}

	// The PLAINTEXT password is hashed here, before it can reach a template. That ordering
	// is the whole protection: RenderInstallerSeed is pure and takes only the hash, so the
	// plaintext exists in this function's memory and nowhere else — not on the volume, not
	// in a rendered file, not in a log line.
	switch {
	case inst.Password_hash != "":
		ctx.PasswordHash = inst.Password_hash
	case inst.Password != "":
		hash, err := hashPasswordSHA512(inst.Password)
		if err != nil {
			return spec.InstallerSeedContext{}, err
		}
		ctx.PasswordHash = hash
	case inst.Defer_provisioning:
		// Correct and intended: an imaging-rig install creates no account, so there is no
		// password to hash. The distro's `when:` guards drop the credentials file.
	default:
		return spec.InstallerSeedContext{}, fmt.Errorf("iso vm: installer needs one of password, password_hash or defer_provisioning — an install with none of them would create an account nobody can log into")
	}

	// The LUKS passphrase is plaintext on the volume by necessity — the installer needs it
	// to create the volume — which makes an encrypted seed a secret. Reuse the login
	// password only when no separate one was given, and say so loudly.
	if inst.Encrypt {
		ctx.EncryptionPassword = inst.Password
		if ctx.EncryptionPassword == "" {
			return spec.InstallerSeedContext{}, fmt.Errorf("iso vm: encrypt: true needs a plaintext password to use as the LUKS passphrase (password_hash alone cannot be used — LUKS takes the passphrase itself, not a hash)")
		}
		fmt.Fprintf(os.Stderr, "WARNING: encrypt: true writes the LUKS passphrase in PLAINTEXT onto the answers volume, and the install is NOT fully unattended — someone must type the passphrase at first boot.\n")
	}

	// SSH keys default to the public half of the per-VM generated key. This is what makes
	// `charly vm ssh` work on an ISO install with no cloud-init anywhere: a stock install
	// of a distro like Omarchy ships sshd DISABLED and the port closed, and it is the
	// presence of an authorized_keys file on the seed that turns both on.
	// BEST-EFFORT, deliberately not fatal. `key_source: generate` (what the shipped VM
	// templates set) always resolves; the failing case is a host with no ~/.ssh key and a
	// spec that never asked for one, where there is simply no key to seed. Hard-failing
	// there would break an ISO build for an operator who intends to use the console — a
	// seeded key is a convenience, not a requirement of installing.
	//
	// It warns rather than passing silently, because the consequence is invisible
	// otherwise: on a distro whose stock install ships sshd disabled and the port closed,
	// the authorized_keys file is what turns both on, so no key means an unreachable guest.
	ctx.SSHAuthorizedKeys = inst.Ssh_authorized_key
	if len(ctx.SSHAuthorizedKeys) == 0 && !inst.Defer_provisioning {
		pubKey, err := resolveSSHPubKeyForSpec(vmSpec, vmStateDir)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "WARNING: no SSH public key could be resolved for the installer seed (%v).\n"+
				"         The guest will be installed with no authorized_keys. On a distro whose stock\n"+
				"         install ships sshd disabled, it will not be reachable over SSH — use the\n"+
				"         console, or set ssh.key_source or installer.ssh_authorized_key.\n", err)
		case pubKey != "":
			ctx.SSHAuthorizedKeys = []string{pubKey}
		}
	}

	return ctx, nil
}

// hashPasswordSHA512 produces a crypt(3) SHA-512 hash ("$6$..."), the form every
// installer in this family expects in its answers file.
//
// It shells out to `openssl passwd -6` deliberately. Go's standard library has no
// crypt(3), golang.org/x/crypto ships bcrypt/scrypt/argon2 but not SHA-crypt, and
// hand-rolling one here would mean charly carrying its own implementation of a
// password-hashing primitive — strictly worse than a host tool dependency. `openssl
// passwd -6` is also the exact command the upstream unattended-install documentation
// tells operators to run, so charly produces byte-identical hashes to a hand-built seed.
func hashPasswordSHA512(plaintext string) (string, error) {
	if _, err := exec.LookPath("openssl"); err != nil {
		return "", fmt.Errorf("hashing the installer password needs `openssl` on PATH (it produces the crypt(3) SHA-512 form the installer expects): %w", err)
	}
	// -stdin, so the plaintext is never an argv entry visible in `ps` to every other user
	// on the host for the lifetime of the call.
	cmd := exec.Command("openssl", "passwd", "-6", "-stdin")
	cmd.Stdin = strings.NewReader(plaintext + "\n")
	out, err := cmd.Output()
	if err != nil {
		// Deliberately does NOT include the command's output: on a failure path that
		// output can echo the input.
		return "", fmt.Errorf("openssl passwd -6 failed: %w", err)
	}
	hash := strings.TrimSpace(string(out))
	if !strings.HasPrefix(hash, "$6$") {
		return "", fmt.Errorf("openssl passwd -6 produced %d bytes that are not a $6$ SHA-512 crypt hash", len(hash))
	}
	return hash, nil
}

// qemuImgCreateBlank allocates an empty sparse qcow2 for an installer to partition.
//
// Unlike qemuImgCreateOverlay there is no backing file: an installer ISO brings its own
// filesystem and writes the disk from nothing. The size is REQUIRED — qemu-img would
// happily create a 0-byte disk, and the installer's failure against one is an
// out-of-space error deep in a partitioner rather than anything naming the cause.
func qemuImgCreateBlank(diskPath, size string) error {
	if size == "" {
		return fmt.Errorf("iso vm: disk_size is required — an installer partitions a blank disk and cannot grow one")
	}
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2", diskPath, size)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("qemu-img create blank %s %s: %w", diskPath, size, err)
	}
	return nil
}
