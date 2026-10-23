---
name: vm
description: |-
  MUST be invoked before any work involving: virtual machines, `charly vm` commands, kind:vm entities in charly.yml, cloud_image vs bootc source types, libvirt/QEMU backends, BIOS vs UEFI firmware, virtio-gpu video, or VM lifecycle.
---

# VM — Virtual Machine Management

## Disposability + deploy cross-ref

- **Disposability is a deployment property, never a VM-entity field.** A `kind: vm` entity carries no `disposable:` / `lifecycle:` field. Put `disposable: true` on the deploy that deploys it. The `vm:` entity only describes VM shape (disk, RAM, SSH, cloud-init, libvirt), never authorization.
- **VM cross-ref**: a `vm:` deploy names its VM entity via the `from:` field (`<bed>: {vm: {from: <entity-name>, disposable: true}}`).
- **An ephemeral VM that automation creates + destroys MUST be named by a `disposable: true` `vm:` DEPLOY — never the entity.** The entity is shape, shared and possibly consumed by other beds; the DEPLOY is the authorization. So a bed or script that does `charly vm create` / `vm snapshot create` / `vm destroy` autonomously declares a per-run `vm:` deploy first:
  ```yaml
  # the ENTITY (shape only — no disposable:)
  my-base-vm:  { vm: { source: { kind: container_disk, image: … } } }
  # the DISPOSABLE DEPLOY the automation drives (create/snapshot/destroy it)
  my-produce: { vm: { from: my-base-vm, disposable: true } }
  ```
  and keys EVERY verb to that deploy's domain with `--domain my-produce` (`vm create`, `vm stop`, `vm snapshot create`, `vm destroy`, and — for the bake — `vm bake --domain my-produce`). `charly vm bake --domain <deploy>` also sets the create's non-destructive `--keep-disk`, because a golden captured on the domain BACKS ONTO the domain's disk: recreating that disk would drop the golden's backing and the bake would boot an empty overlay. A bed that instead destroys the shared entity (or a domain it was not handed as `disposable: true`) is a DISP violation (the validator's disposable-only check) — authorization is derived from the destroyed TARGET, never from the bed's own name or its `lifecycle:` tag.
- **`charly update <vm-entity-name>`** does NOT gate on `disposable:` — an explicit invocation rebuilds ANY target (destroy→create the domain, reuse the qcow2 disk unless `--build`, then re-apply the deploy's layers via the shared `deploy add` path). For a non-disposable, non-ephemeral target it prints a one-line transparency note (`noteUpdateDisposability`) and proceeds. The `disposable: true` flag stays load-bearing as the authorization for UNATTENDED autonomous destroy + rebuild (the project rulebook R10 (`AGENTS.md`)) and the check-runner's unattended fresh rebuild, NOT as an `charly update` capability check. See `/charly-internals:disposable` and `/charly-core:charly-update`.

## Overview

`charly vm` commands build disk images and manage virtual machines via libvirt (default) or direct QEMU. VMs are declared as **`kind: vm` entities in the unified `charly.yml`** — a first-class primitive alongside `candy:` image entries, each a name-first top-level node (`<name>: {vm: …}`). Two source types:

- **`source.kind: cloud_image`** — fetches a pre-built qcow2 from an external URL (Arch, Fedora, Ubuntu, Debian, CentOS Cloud images). Renders a NoCloud seed ISO with cloud-init. Canonical example: `/charly-vm:arch-cloud-vm`.
- **`source.kind: bootc`** — pairs (via its `box:` source field) with a `candy:` image entry that has `bootc: true`. Runs `bootc install to-disk` inside a privileged container.
- **`source.kind: bootstrap`** — builds a rootfs from PACKAGES with the distro's own bootstrapper (pacstrap / debootstrap / dnf) inside a privileged builder box, partitions a disk, and installs the distro's bootloader. Canonical examples: `/charly-vm:cachyos-bootstrap-vm`, `/charly-vm:debian-debootstrap-vm`.
- **`source.kind: iso`** — boots a distro's OWN installer ISO with a charly-rendered answers volume, so the distro installs itself with nobody at the keyboard. The answer FORMAT belongs to the distro (`#DistroInstaller`); the answer DATA belongs to the VM entity (`installer:`). The blank disk is FIRST in the boot order, so the firmware falls through to the ISO on the first boot and the installed system boots from disk ever after — no eject, no detach, and no reboot-loop hazard. Canonical example: the `omarchy-vm` skill (family `vm`).
- **`source.kind: clone`** — builds a NEW VM on top of ANOTHER VM: `from_vm:` + `from_snapshot:` name the base entity and the exact snapshot of it to use. `charly vm build` materializes a fresh per-VM qcow2 with the parent snapshot's external disk as backing chain (qemu-img overlay), bumps the parent snapshot's refcount, and regenerates the cloud-init seed ISO with a fresh instance-id (`cloud_init_clean: true` additionally injects `cloud-init clean --machine-id --logs` so machine-id + SSH host keys regenerate on first boot). This is the VM-on-VM layering primitive — the VM analog of a container image's `FROM <base>`.

VMs are not configured on `candy:` image entries — `vm:` / `libvirt:` on a `candy:` image are rejected at load time. `bootc: true` stays on a `candy:` image entry to mark it bootable. The legacy on-image `vm:`/`libvirt:` fields predate the schema floor and are no longer migratable — a config still carrying them must be re-authored as a name-first `kind: vm` node (see `/charly-build:migrate`). For the YAML authoring reference, see `/charly-vm:vms-catalog`; for the Go types, see `/charly-internals:vm-spec`.

## Quick Reference

| Action | Command | Description |
|--------|---------|-------------|
| Build QCOW2 | `charly vm build <name>` | Build disk image (`<name>` = kind:vm entity key) |
| Build RAW | `charly vm build <name> --type raw` | Build RAW disk |
| Create VM | `charly vm create <name>` | Provision libvirt/QEMU VM from the built disk |
| Start VM | `charly vm start <name>` | Start a stopped VM |
| Stop VM | `charly vm stop <name>` | Graceful shutdown |
| Force stop | `charly vm stop <name> --force` | Force stop (destroy) |
| Destroy VM | `charly vm destroy <name>` | Remove VM definition |
| Destroy + disk | `charly vm destroy <name> --disk` | Remove VM and delete disk |
| List VMs | `charly vm list [-a]` | List running (or all) VMs |
| Console | `charly vm console <name>` | Attach to serial console |
| SSH | `charly vm ssh <name>` | SSH into VM |
| GPU passthrough readiness | `charly vm gpu status` | Host IOMMU + vfio-pci + per-GPU **per-function** group/driver/mode report (flags wedge/poison) |
| GPU passthrough block | `charly vm gpu list` | Emit a ready-to-paste `libvirt.devices.hostdevs:` block (whole IOMMU group, `managed: "yes"`) |
| GPU driver mode (switch) | `charly vm gpu mode [vfio\|nvidia]` | Flip the WHOLE IOMMU group (display→nvidia/vfio, audio→snd_hda_intel/vfio); omit the arg to SHOW the mode |
| GPU recover | `charly vm gpu recover` | Rebind an unbound/half-switched card to vfio-pci; reports **reboot-required** and does nothing on a true device_lock wedge |
| Load image into guest | `charly vm cp-box <vm> <ref> [--as <tag>]` | `podman save \| ssh … podman load` a host image into a running guest — streamed, no intermediate tarball, verified-idempotent |
| Clone VM | `charly vm clone <new> --from <src>[@<snap>]` | Write a `source.kind: clone` declaration + build the clone disk (VM-on-VM; `--from arch` auto-snapshots, `--from arch@golden` names the snapshot) |
| Snapshot create | `charly vm snapshot create <vm> <name>` | External (clone-friendly) by default; `--mode internal` embeds in the disk |
| Snapshot create-consistent | `charly vm snapshot create-consistent <vm> <name>` | Guest-consistent (guest-agent fsfreeze → snapshot → thaw; strict, no silent fallback) |
| Snapshot capture-declared | `charly vm snapshot capture-declared <vm>` | Capture every snapshot declared in the entity's `snapshot:` block (idempotent) |
| Snapshot list/delete/revert | `charly vm snapshot {list,delete,revert,promote} <vm> <name>` | Registry + refcounts; delete refuses while clones/ephemerals reference it |

VM name convention: `charly-<name>[-<instance>]`. Default libvirt URI: `qemu:///session`.

## GPU passthrough (VFIO)

**Preempting a GPU holder is standing-authorized.** When a GPU deploy of a VM
declares `requires_exclusive: [nvidia-gpu]`, the arbiter gracefully stops the operator's
running `preemptible` GPU-workstation VM to free the card and is GUARANTEED to restart it —
you do this autonomously, with no per-run confirmation (preemption is reversible by
design; see `/charly-internals:disposable` "Standing authorization").

To pass a physical GPU through to a VM and (e.g.) run a CUDA container inside it:

1. **Host readiness — and why host `nvidia-smi` is the WRONG check.** `charly vm gpu
   status` confirms IOMMU is on (`intel_iommu=on` / `amd_iommu=on iommu=pt` on the
   kernel cmdline) and shows each GPU's IOMMU group + current driver; `charly doctor`
   reports the same under "VFIO / GPU passthrough". On a passthrough-READY host the
   card is bound to the **`vfio-pci`** host driver (NOT `nvidia` / `nouveau` /
   `amdgpu`) precisely so a guest can claim it — so host-side `nvidia-smi` /
   `rocm-smi` / `nvidia-settings` **FAIL BY DESIGN**. That failure is the *ready*
   state, NOT a "no GPU" state: never gauge GPU- or passthrough-bed availability from
   host `nvidia-smi`. The authoritative host checks are `charly vm gpu status` /
   `charly vm gpu list` (or `lspci -nnk` showing `Kernel driver in use: vfio-pci`);
   the GPU comes alive INSIDE the guest, where the in-guest driver layer (step 3)
   binds it and in-guest `nvidia-smi` works.
2. **The hostdev is AUTO-ALLOCATED.** When a deploy of a VM declares
   `requires_exclusive: [<token>]` and that token maps to an embedded
   `resource:` vocabulary entry with a `gpu:` selector (e.g. `resource: {nvidia-gpu: {gpu:
   {vendor: "0x10de"}}}`), `charly vm create` auto-detects the matching GPU,
   writes its whole-IOMMU-group `hostdevs:` block (each `managed: "yes"`) into
   this host's per-domain `instance.yml`, and injects it — or **FAILS HARD** if
   the required card is absent (`autoAllocateExclusiveGPUs`, gpu_allocate.go).
   No manual step. The committed `kind: vm` entity stays portable (no PCI
   address); set `firmware: uefi-insecure` + `backend: libvirt` on it (the qemu
   backend does not render `<hostdev>`). An operator-authored `hostdevs:` block
   (committed `charly.yml` OR `instance.yml`) ALWAYS wins — auto-allocation defers
   to it (no double-inject, no re-detection; delete the block to re-detect).
   `charly vm gpu list` still prints a ready-to-paste block for the manual
   override path; `charly vm gpu status` reports host readiness. See
   `/charly-internals:disposable` "resource-arbitration axis" + `/charly-build:build`
   `resource:`.
3. **In-guest driver** — a passthrough guest needs the actual kernel module, not
   just the userspace container toolkit. Apply a kernel-driver layer (it
   blacklists the in-tree driver, regenerates the initramfs, and declares
   `reboot: true` so `charly deploy add vm:<name>` reboots the guest and waits for it
   to return). The `nvidia`-toolkit layer + `nvidia-ctk cdi generate` then makes
   the GPU available to containers via CDI (`--device nvidia.com/gpu=all`).

Code-43 workarounds for consumer NVIDIA cards are first-class libvirt fields:
`libvirt.features.kvm.hidden: on` and `libvirt.features.hyperv.vendor_id`.
Optional per-hostdev `rom: {bar: off}` / `driver: {name: vfio}` are supported.
Worked end-to-end example: the CachyOS `check-cachyos-gpu-vm` bed (see
`/charly-vm:cachyos-bootstrap-vm`, `/charly-check:check`).

## GPU driver-mode switch (vfio ↔ nvidia) — and the device_lock hazard

A single passthrough-capable NVIDIA card serves EITHER a VM (every IOMMU-group
function on `vfio-pci`) OR many shared CDI pods (display function on `nvidia`,
HDMI-audio on `snd_hda_intel`), one mode at a time. The arbiter flips it
automatically for `requires_exclusive` (→ vfio) / `requires_shared` (→ nvidia)
claims (see `/charly-internals:disposable`); `charly vm gpu mode [vfio|nvidia]`
is the manual operator override and `charly vm gpu status` the honest inspector
(per-function driver + IOMMU group + mode). **The switch is group-aware** — it
moves EVERY function of the IOMMU group together, because a VFIO group is
guest-usable only when ALL its members are vfio-bound
(`VFIO_GROUP_FLAGS_VIABLE`); flipping only the display function (the old
behavior) stranded the audio function and broke group viability. It uses
`driver_override` + `drivers_probe` to force the exact target driver regardless
of the boot-time `ids=` dynamic-id table.

### The device_lock wedge (why the nvidia→vfio switch is careful)

Unbinding the `nvidia` driver via **sysfs `unbind` while any client still holds
the GPU** hangs the host, reboot-only. Source-confirmed (NVIDIA open driver
`kernel-open/nvidia/nv-pci.c`) + RDD-proven on this host:

- `nv_pci_remove` (the PCI `.remove`, fired by sysfs `unbind` / `rmmod`) calls
  `nv_pci_remove_helper(block_if_gpu_in_use=TRUE)`, which — when
  `nvl->usage_count != 0` (an open `/dev/nvidia*` fd, a live CUDA context, or
  `nvidia_uvm`/`nvidia_modeset`/`nvidia_drm` attached) — BLOCKS FOREVER in
  `while (usage_count != 0) os_delay(500)`. The Linux PCI core holds the
  per-device **`device_lock`** (`dev->mutex`, an uninterruptible mutex) across
  the *entire* `.remove`, so every later bind/reset/remove on that device
  serializes behind it in unkillable `D` state.
- Recovery is **REBOOT-ONLY**: no userspace primitive releases a held
  `device_lock` or aborts an in-kernel driver callback. A sysfs `reset`
  (`pci_reset_function`) and `echo 1 > remove` re-take the same lock and also
  `D`-state; `kill`/`timeout` cannot touch a `D`-state task; a raw `setpci`
  secondary-bus-reset bypasses the lock but fixes neither the mutex nor
  `usage_count` and risks an MCE.
- It is **NOT** power/runtime-PM related (a common wrong guess — do not invoke
  `power/control` / `d3cold` to explain it): `usage_count` is a plain
  open-reference count. And `nv_pci_shutdown` (host reboot/shutdown) passes
  `block=FALSE` and early-returns, so a host reboot NEVER wedges on a busy GPU.

### Why the switch is safe by construction

The switch NEVER sysfs-unbinds a busy nvidia. The nvidia→vfio path detaches via
**`modprobe -r`**, which is module-refcount-guarded — it returns `EBUSY`
*immediately* if any client holds the GPU (it never reaches the blocking
`.remove` loop), so module-unload IS the deterministic preflight gate. Before the
flip the arbiter first RECLAIMS charly's OWN running GPU-CDI pods that hold no
active lease (a leaked/stray disposable bed — detail: `/charly-internals:disposable`
"The mode flip"), so a residual `EBUSY` is a GENUINE EXTERNAL (non-charly) client;
against that remaining client the switch REFUSES rather than force-unbinding. The host-side
precondition is fully verifiable WITHOUT touching the device: `lsmod` (nvidia
refcount + dependents — the only proxy that reflects the in-kernel
`nvidia_dev_get` refs of uvm/modeset/drm), `lsof /dev/nvidia*`, and
`nvidia-smi --query-compute-apps`; NEVER open `/dev/nvidia*` to probe (that
increments `usage_count`). The whole switch runs under a bounded deadline; if a
GSP-teardown stall ever wedges it anyway, the arbiter **POISONS** the resource
(keyed to the current boot id) so no later claimant re-wedges the card —
`charly vm gpu status` flags it and acquires are refused with a reboot-required
error until the host reboots.

### `charly vm gpu recover`

Recovers a card left **unbound / half-switched** (e.g. an interrupted flip) back
to the clean vfio-pci default — but ONLY when the card is NOT wedged. It first
runs a read-only probe for a true device_lock wedge (a `D`-state task in
`nv_pci_remove`); on a wedge it reports **reboot-required and attempts NOTHING**
(a bind on a wedged card would add a second permanent `D`-state). On a healthy
card it rebinds the whole group to vfio-pci and clears any stale poison marker.

Implementation + full RCA: `candy/plugin-gpu/switch.go` (the switch primitive, cutover
C9 — reached from core via the plugin's `verb:gpu` dispatch; the former
`charly/gpu_shim.go` driver-switch shims are DELETED, K-wave 2 cone R3 — the file now
holds only `gpuProbeReply` + `DetectVFIO`), the arbiter's
poisoning in `candy/plugin-preempt` (`arbiter_support.go`), `candy/plugin-vm/vm_gpu_cmd.go`
(status/recover/plan); the arbiter side is `/charly-internals:disposable`
"resource-arbitration axis".

## Building Disk Images

```bash
charly vm build arch          # cloud_image: fetch qcow2, resize, render seed ISO
charly vm build <bootc-vm> --type qcow2      # bootc: bootc install to-disk
charly vm build <name> --size 40G        # disk_size override (CLI wins over charly.yml)
charly vm build <name> --root-size 10G   # bootc only: cap root partition
charly vm build <name> --console         # enable console output for debugging
charly vm build <name> --transport containers-storage   # bootc: local podman storage
```

Output: `output/<type>/disk.<type>`. For cloud_image, also `output/<type>/seed.iso`. ISO format is not supported — use qcow2 or raw.

The `--transport` flag (bootc only) controls how the container image is read: `registry` (pull from registry), `containers-storage` (local podman), `oci` (OCI dir), `oci-archive` (OCI tarball).

Source: `candy/plugin-vm/vm_build.go`, `candy/plugin-vm/vm_cloud_image.go` (the former `charly/vm_build.go`/`charly/vm_cloud_image.go` are DELETED, K-wave 2).

## VM Lifecycle

```bash
charly vm create arch                        # default resources from charly.yml
charly vm create <name> --ram 8G --cpus 4               # CLI overrides
charly vm create <name> --ssh-key auto                  # auto-detect SSH key (default)
charly vm create <name> --ssh-key generate              # generate new keypair
charly vm create <name> --ssh-key none                  # no key injection
charly vm create <name> --ssh-key ~/.ssh/id_ed25519.pub # specific key
charly vm create <name> -i prod                         # named instance
charly vm start <name>
charly vm stop <name>                                   # graceful
charly vm stop <name> --force                           # destroy
charly vm destroy <name>                                # remove VM definition
charly vm destroy <name> --disk                         # also delete disk image
charly vm list [-a]
charly vm console <name>                                # serial console
charly vm ssh <name>                                    # SSH with resolved user/port from charly.yml
charly vm ssh <name> -p 2224 -l arch                    # override user/port
charly vm ssh <name> -- ls /tmp                         # run command via SSH
```

**`charly vm create` regenerates the cloud-init seed ISO** (cloud_image sources
only). Edits to the VM entity's `cloud_init:` block — runcmd entries, packages,
charly_install strategy, network-config — take effect on the next `charly vm create`
without requiring a full `charly vm build`. The qcow2 disk is left alone; only
the seed ISO is rewritten (via `RegenerateSeedISO` in `candy/plugin-vm/vm_cloud_image.go`).
Use `charly vm destroy --disk` + `charly vm build` when you need a truly fresh disk
(e.g. when a previous failed cloud-init left stale state in `/home/<user>/`).

**`autostart: true`** on the entity makes `charly vm create` set libvirt's domain
autostart flag AND wire the session-boot trigger (`loginctl enable-linger` + a
generated per-VM user unit `charly-autostart-<domain>.service` that `virsh start`s
the domain at boot), so the VM starts at host boot. Requires the libvirt backend.
**`disk_size: 1T`** (or larger) is cheap — the qcow2 is sparse, so the virtual
size is just a ceiling and physical bytes grow on demand. A host directory can be
shared into the guest via `libvirt.devices.filesystems` (virtiofs) and mounted by
the `/charly-distros:workspace-mount` layer. See `/charly-vm:vms-catalog`.

## `kind: vm` entity reference

Canonical node-form shape, condensed from `/charly-vm:vms-catalog`. Each VM is a
name-first top-level node, and EVERY block — `libvirt:` included — lives INLINE in the
`vm:` kind value. The former named child-node shape (`<entity>-<key>`) was deleted by
the schema-compaction cutover and is now a hard loader error:
`no kind discriminator — collections and plan steps live INLINE in the kind value`.

```yaml
# cloud_image source
arch:
  vm:
    source:
      kind: cloud_image
      url: https://fastly.mirror.pkgbuild.com/images/latest/Arch-Linux-x86_64-cloudimg.qcow2
      checksum: {type: sha256}                      # value auto-resolves from <url>.SHA256 sidecar
      base_user: arch                               # adopt pattern (no useradd)
      distro: arch            # **REQUIRED on a cloud_image source.** Once this cutover lands, the vm kind's OpValidate
                                                   # rejects a source that omits it. It is a closed `#DistroID`, and
                                                   # `spec/schema/distro_vocab.cue` is the single source for the id space and
                                                   # each id's package format, sshd unit and init system. Nothing is inferred
                                                   # from `base_user`, an image URL, or the source kind: the earlier base_user
                                                   # inference is DELETED, so an unnamed distro is an author-time error rather
                                                   # than a silent wrong default. The 13-id vocabulary covers the pacman family
                                                   # (arch/archarm/manjaro/endeavouros/cachyos), the RPM family
                                                   # (fedora/rhel/centos/rocky/almalinux), Debian-family (debian/ubuntu) and
                                                   # alpine — so naming a near relative as a workaround is no longer needed for
                                                   # any of them. Alpine is first-class: `distro: alpine` selects apk and
                                                   # OpenRC. openSUSE is still absent and has no near relative (zypper);
                                                   # adding it is one entry in that file.
                                                   # Note also that #DistroID (14 ids, omarchy included) and the embedded `distro:` BUILD vocabulary are
                                                   # different sets, and the gap is SILENT: `buildVmSyntheticBox` resolves this field against
                                                   # the build vocabulary and on a miss leaves `img.Pkg` unset, so candy installation compiles
                                                   # ZERO package steps while `deploy add` reports success. A schema-valid id is therefore not
                                                   # automatically a resolvable one.
                                                   # Use a bare id: `ResolveDistro` strips at `:` but the cloud-init
                                                   # dispatches compare exactly, so `debian:13` yields `openssh`. (That sentence is about the
                                                   # BUILD vocabulary — `ResolveDistro` in `sdk/buildkit/config_resolve.go` — not `#DistroID`.)
    disk_size: 40G
    ram: 8G
    cpu: 4
    machine: q35
    firmware: bios                                  # BIOS preferred for cloud images — see decision matrix below
    network: {mode: user}
    ssh: {port: 2224, key_source: generate}
    cloud_init:
      package: [sudo, spice-vdagent]
      charly_install: {strategy: auto}
    libvirt:
      devices:
        video: [{model: virtio, vram: 65536, heads: 1, accel3d: false}]
        graphics: [{type: spice, autoport: "yes", listen: 127.0.0.1}]

# bootc source
my-bootc:
  vm:
    source:
      kind: bootc
      box: my-bootc                               # `box:` source field → a `candy:` image entry with bootc: true
    disk_size: 80 GiB
    ram: 16G
    cpu: 6
    ssh: {user: root, port: 2250}
```

Every field in `VmSpec` applies to both source kinds (parity guarantee). See `/charly-internals:vm-spec` for the Go type reference and `/charly-vm:vms-catalog` for the full field-by-field authoring guide.

## Backend matrix

Resolution order: **libvirt → qemu** (auto-detected). Override via `charly settings set vm.backend libvirt`.

| Backend | Requires | When to pick |
|---|---|---|
| `libvirt` (default) | libvirt session daemon socket | Almost always — full domain XML, portForward with passt, SPICE console, per-VM NVRAM |
| `qemu` | `qemu-system-*` binary | Direct QEMU; air-gapped hosts where libvirt isn't installed |

**`vm start` is idempotent on BOTH backends — the qemu backend's
already-running guard is `vmshared.QemuAlive`.** The libvirt
backend checks `already_running`; the qemu backend checks
`vmshared.QemuAlive(stateDir)` (read `<stateDir>/qemu.pid` →
`Signal(0)` → `/proc/<pid>/cmdline` must carry `-pidfile
<stateDir>/qemu.pid` as adjacent NUL-separated argv fields — the
ownership match confirms the live process is the qemu started for
THAT state dir, closing the reused-PID false-positive a bare
`Signal(0)` would report as "running" and the recycled-onto-
another-VM case a `qemu-system` substring check would accept). A
live QEMU recorded in the pidfile makes `vm start` a clean no-op
(`VM <name> is already running`). This is load-bearing for the
rebuild path (`vmRebuild`): `vm create` already starts the domain,
and the ensure-running guard must be a no-op for a running VM.
The bug this fixed: the qemu backend used to blindly re-execute
the stored command, so after `vm create` the guard started a
SECOND QEMU, which failed to lock the pidfile (`cannot create PID
file: Cannot lock pid file: Resource temporarily unavailable`) and
corrupted the VM's state — every `charly check run <vm-bed>`'s
`update` step failed with this error, even running sequentially.
The same `vmshared.QemuAlive` probe drives the `vm list` qemu
state scan and the preempt arbiter's holder probe (R2, one
liveness probe).

**Socket path probing for libvirt ≥ 8.0**: modular libvirt splits into `virtqemud` / `virtnetworkd` / ... — the socket is `virtqemud-sock`, not `libvirt-sock`. `libvirtSessionSocket()` probes `virtqemud-sock` first, falls back to `libvirt-sock` on older setups. Symptom of a misprobe: `ConnectToURI(QEMUSession)` returns `End of file while reading data: Input/output error` — means the daemon isn't accepting on the socket you reached.

### Prereq: libvirt user-session daemon

`backend: libvirt` requires `virtqemud.service` (or older monolithic
`libvirtd.service`) running as a **user**-service — no sudo needed:

```bash
systemctl --user enable --now virtqemud.service
```

`resolveVmBackend()` itself best-effort spawns the session daemon
(`startLibvirtUserSession()`: `systemctl --user start virtqemud.service` →
`libvirtd.service` → a `virsh -c qemu:///session list` autospawn) **before**
probing for its socket, so EVERY vm verb (create / build / start / stop /
destroy / console) detects libvirt uniformly — not just `charly vm create`.
This matters because many hosts (Arch / CachyOS) ship **no persistent
`virtqemud.socket`**: the session socket exists only AFTER a client triggers
libvirt's on-demand autospawn, so a COLD socket probe (run before any spawn)
false-negatives a fully working libvirt and silently falls back to qemu — the
bug that made the per-verb detection nondeterministic. If the unit isn't
installed at all (libvirt missing entirely), `resolveVmBackend()` surfaces the
explicit error `"libvirt backend requires libvirt session daemon"` with the
remediation hint above.

**Do NOT gauge libvirt availability with `systemctl is-active
<virtqemud|libvirtd>.service`.** A socket-activated OR on-demand-autospawn
daemon reports the SERVICE `inactive` even while libvirt is fully usable (it
spawns per connection with `--timeout` and exits when idle, so the *service*
is idle between calls). The only valid signals are the **socket**
(`$XDG_RUNTIME_DIR/libvirt/virtqemud-sock`) or a real connection
(`virsh -c qemu:///session list`). The system/rootful path (`qemu:///system`,
`libvirtd.socket`) is a SEPARATE daemon charly does not use — charly is
rootless-first (`qemu:///session`), so a non-root user with only the system
daemon still needs the user-session path (which `resolveVmBackend()` autospawns).

For projects whose check beds use the `libvirt:` and `spice:` check verbs
(e.g., the project's `arch:` VM template), pin the
backend explicitly via `backend: libvirt` on the kind:vm entity —
`backend: auto` would silently fall through to qemu when the daemon
is missing, breaking every libvirt-RPC probe with a confusing
"no such file or directory" error 5+ minutes into the check-live
timeout. `arch:` and `k3s-vm:` carry this pin (see `/charly-check:check`
on disposable VM beds).

Source: `candy/plugin-vm/vm_backend_resolve.go` (`resolveVmBackendPlugin`, over `vmshared.StartLibvirtUserSession` — the former `charly/vm_backend_lifecycle.go` is DELETED, K-wave 2),
`candy/plugin-vm/vm_libvirt.go`, `candy/plugin-vm/vm_qemu.go`.

## BIOS vs UEFI decision matrix

The `firmware:` field controls the VM boot path. Choice matters more than most authors realize.

| Firmware | When to pick |
|---|---|
| `bios` (default) | Cloud images that ship a GPT BIOS boot partition — **Arch, Fedora Cloud, CentOS Cloud are proven on beds** (`check-arch-repo`, `check-fedora-repo`, `box/fedora`). Avoids stale BOOTX64.EFI issues. No OVMF package dependency. No per-VM NVRAM. See `/charly-vm:arch-cloud-vm` Finding B for the RCA. |
| `uefi-insecure` | bootc Fedora images (ship signed BOOTX64.EFI built at image-build time). **Debian and Ubuntu cloud images** — `debian-13-generic-amd64.qcow2` does NOT boot under `bios` (measured below), and `box/debian` + `box/ubuntu` already pin this. Guests needing GPT > 2 TiB disks. ARM hosts (aarch64). |
| `uefi-secure` | Guests needing Secure Boot (signed kernel + initramfs). Locks to Microsoft UEFI CA keys. |

See `/charly-internals:ovmf` for the per-distro OVMF_CODE/OVMF_VARS path table and the per-VM NVRAM mechanics. When `firmware: bios`, `ResolveOvmfForSpec` returns empty strings and the libvirt renderer skips `<loader>`/`<nvram>` entirely.

**Debian/Ubuntu cloud images are the correction to the row above, and it was measured, not reasoned.**
`debian-13-generic-amd64.qcow2` under `firmware: bios` stalled a bed twice; the same bed passed
(`check-debian-repo` PASS, steps=9) with `firmware: uefi-insecure` and nothing else changed. The
guidance to prefer `bios` still holds for Arch/Fedora/CentOS — `box/fedora` runs `bios` today — so
this is a per-image property, not a global preference, and the honest test is whether the specific
image boots.

**The signature of a firmware mismatch, because it looks like anything but firmware.** The domain
starts and `charly vm list` reports it `running`; the port forward ACCEPTS TCP (qemu user-net binds
the forward before the guest listens); `sshd` never answers, giving
`Connection timed out during banner exchange` or `kex_exchange_identification: Connection reset by
peer`; the serial console is silent; and the deploy step waits indefinitely with no next-step log.
The discriminating probe is the **deployed disk's growth**, read twice:

```bash
stat -c '%s %y' ~/.local/share/charly/vm/charly-<domain>/disk.qcow2   # twice, ~20s apart
```

A booted guest's mtime ADVANCES (it may sit at a few hundred MB while it does); a guest that never
booted stays frozen at ~200 KB. **Size alone cannot separate them** — every deployed disk starts as
a small qcow2 overlay backed by the cached base image, so it grows only as the guest writes, and a
2 GB disk means "booted and installed packages" rather than "image materialized". Two traps inside
this probe: `qemu-img info` on a RUNNING guest's disk fails with `Failed to get shared "write" lock`
(that is the VM holding it, not a corrupt image), and the presence of `nvram.fd` in the VM's state
dir confirms the UEFI path applied — but it is REMOVED when a bed completes and cleans up, so its
absence after a passing run says nothing.

## Video-model choice

`libvirt.devices.video[0].model` drives the VM's paravirtualized GPU.

| Model | Default for | Why |
|---|---|---|
| `virtio` (virtio-gpu) | **Linux guests — modern default** | Native `virtio_gpu` kernel DRM driver (4.16+), Wayland-native, simpler config, no X-server-specific driver needed |
| `qxl` | Legacy X11 SPICE use | 2010-era Red Hat SPICE stack; simpledrm→qxldrmfb takeover race under UEFI (see `/charly-vm:arch-cloud-vm` Finding B); more knobs (ram_size + vram_size + vram64_size_mb + vgamem_mb) |
| `cirrus` | BIOS fallback only | Low resolution, no acceleration |
| `none` | Headless VMs | No framebuffer at all |

SPICE is video-model-agnostic — it streams pixels from virtio-gpu as well as from QXL. See `/charly-internals:libvirt-renderer` "video-model choice" for the full decision rationale.

## SSH Key Injection

SSH keys inject via two additive channels (both on by default for cloud_image VMs):

- **SMBIOS type 11 OEM strings** — systemd-ssh-generator (systemd ≥ v250) materializes the pubkey into `~<user>/.ssh/authorized_keys` during early boot. Works even without cloud-init.
- **cloud-init user-data** — `users:[...].ssh_authorized_keys` list in the rendered user-data on the NoCloud seed ISO.

The `--ssh-key` flag (and the entity's `vm.ssh.key_source`) controls the source of the pubkey:

| Value | Behavior |
|-------|----------|
| `auto` (default) | Uses first `~/.ssh/*.pub` found |
| `generate` | Creates new keypair in `~/.local/share/charly/vm/charly-<name>/id_ed25519`. Idempotent across rebuilds. |
| `none` | No key injection |
| `/path/to/key.pub` | Uses the specified public key |

See `/charly-internals:vm-spec` for the `VmSsh.KeyInjection` tri-state (`auto` | `enabled` | `disabled`) per channel, and `/charly-internals:cloud-init-renderer` for the `composeUsers` adopt-merge pattern that delivers the key into the guest.

## User-Level Defaults

Set via `charly settings set`:

| Key | Env Var | Default |
|-----|---------|---------|
| `vm.backend` | `CHARLY_VM_BACKEND` | `auto` |

Per-VM overrides live on the VM entity in `charly.yml`. The user-level defaults exist only for fields that don't have a per-VM equivalent (backend is the main one). For anything else, declare it on the `<name>` VM entity (`<name>.vm.…`).

**`CHARLY_VM_STATE_DIR`** (env-only, no `charly settings` key — `vmshared.VmStateRoot()`) overrides the root directory for per-VM host state (`~/.local/share/charly/vm/<domain>/` by default — ssh keys, known_hosts, seed ISO, the per-domain disk overlay, `snapshots/`, `instance.yml`). Closes the "global, non-worktree-scoped VM state dir" footgun: a libvirt domain name derives purely from the deploy name, with no project/worktree component, so two concurrent `git worktree` checkouts of this repo running the SAME bed/deploy name (the common case — bed names like `check-charly-vm` are fixed identifiers shared across every worktree) collide on the same host state directory AND the same libvirt domain. Set it to a worktree-distinct absolute path (e.g. in a per-worktree wrapper script or `.envrc`) to isolate concurrent worktrees; must be absolute — a relative override is rejected rather than silently resolving against whatever cwd happens to be active. Unset (the default) is byte-identical to the pre-fix behavior.

## Libvirt XML configuration

Primary surface is the structured `LibvirtDomain` on the VM entity's own INLINE `libvirt:` field (`<name>: {vm: {…, libvirt: {features, cpu, clock, devices, sysinfo, launch_security, …}}}` — `#Vm.libvirt?: #LibvirtDomain`). See `/charly-internals:libvirt-renderer` for the full schema.

Raw-XML escape hatch: the VM entity's inline `libvirt.snippets:` (list of strings) — classified by element name. Device-scoped elements go into `<devices>`, domain-scoped before `</domain>`. Deduplicated by exact string match.

Raw XML passthrough: `libvirt.snippets:` and `libvirt.xml_passthrough:` are typed-open escape hatches INSIDE the VM entity's own `libvirt:` block. The CANDY-level `libvirt:` raw-snippet field was REMOVED (zero live Go consumers, retired alongside the box-level libvirt hard cutover — see the note at `spec/schema/candy.cue`), so a layer can no longer contribute device XML that way; authoring it now fails closed with `libvirt: field not allowed`. Box-level `libvirt: [...]` is likewise not a valid field — VM XML lives on the VM entity's inline `libvirt:`.

Source: `candy/plugin-vm/libvirt.go` (`InjectLibvirtXML` — the former `charly/libvirt.go` is DELETED, K-wave 2), `sdk/vmshared/libvirt_yaml.go`, `candy/plugin-vm/libvirt_yaml_bridge.go`.

## Common Workflows

### Build and run a cloud_image VM

```bash
charly vm build arch              # fetches qcow2, resizes, renders seed ISO
charly vm create arch
ssh -p 2224 -i ~/.local/share/charly/vm/charly-arch/id_ed25519 arch@127.0.0.1
```

### Build and run a bootc VM

```bash
charly box build <bootc-image>       # container image must exist first
charly vm build <bootc-vm>
charly vm create <bootc-vm>
charly vm start <bootc-vm>
charly vm ssh <bootc-vm>
```

### Build a VM on top of another VM (clone)

The `source.kind: clone` path builds a NEW VM from an EXISTING VM's
snapshot — the VM-on-VM layering primitive (the VM analog of a container
image's `FROM <base>`). The base is named by entity + snapshot, so the
exact base state is pinned:

```bash
# 1. Provision the base VM and capture a named snapshot of its state.
charly vm build base-vm
charly vm create base-vm
charly vm ssh base-vm -- echo READY          # wait for first boot + cloud-init
charly vm snapshot create-consistent base-vm golden   # guest-consistent capture

# 2. Declarative alternative: the entity's snapshot: block declares the
#    snapshots; capture-declared captures them (idempotent).
#    base-vm: { vm: { ..., snapshot: [{name: golden}] } }
charly vm snapshot capture-declared base-vm

# 3. Clone: writes a source.kind: clone declaration + builds the disk.
charly vm clone clone-vm --from base-vm@golden
#    (or author the entity by hand and:)
charly vm build clone-vm
charly vm create clone-vm
charly vm ssh clone-vm -- echo CLONE_READY
```

The clone disk is a qcow2 overlay whose backing file is the parent
snapshot's frozen external disk — the base bytes are shared, not copied
(the parent snapshot's refcount is bumped; `charly vm snapshot delete`
refuses while clones reference it). `cloud_init_clean: true` (the
`charly vm clone` default) regenerates machine-id + SSH host keys on
first boot so two clones never collide. The snapshot registry lives at
`~/.local/share/charly/vm/charly-<vm>/snapshots/registry.json`; the
snapshot commands accept `--domain <deploy>` to target a check bed's
per-deploy domain.

### Apply layers inside a VM

```bash
charly vm create arch
charly deploy add vm:arch ripgrep         # apply ripgrep layer over SSH
charly deploy add vm:arch fedora-coder --add-candy team-extras
charly deploy del vm:arch                 # reverse all applied layers
```

See `/charly-core:deploy` "vm: target" for the `charly deploy add vm:<name>` surface and `/charly-internals:vm-deploy-target` for the executor model.

### Debug VM boot issues

```bash
charly vm build <name> --console     # enable console output during disk build
charly vm create <name>
charly vm start <name>
charly vm console <name>             # watch boot messages
```

### Multiple VM instances

```bash
charly vm create <name> -i dev
charly vm create <name> -i staging
charly vm start <name> -i dev
charly vm ssh <name> -i dev
```

## Known live-tested caveats

Non-obvious issues that surface only when VMs actually boot. `/charly-vm:arch-cloud-vm` is the canonical cloud_image worked example; the bootc-specific caveats below apply to any `source.kind: bootc` VM.

### Privileged container needs `-v /dev:/dev` (bootc)

`charly vm build` runs `bootc install to-disk --via-loopback` inside a privileged container. `--privileged` alone is not enough — the container's `/dev` is a tmpfs in its own mount namespace, so `losetup`'s `LOOP_CTL_GET_FREE` creates a `/dev/loopN` in the kernel but the node is invisible inside the container. `BuildBootcVM` (`candy/plugin-vm/vm_bootc_engine.go`, via `buildkit.RunPrivileged`) bind-mounts `/dev` so the container shares the host's `/dev` (the former `charly/vm_build.go` is DELETED, K-wave 2).

### virtqemud socket probing (libvirt ≥ 8.0)

Modular libvirt daemons: the session socket moved from `libvirt-sock` to `virtqemud-sock`. Probe order in `libvirtSessionSocket()`: virtqemud-sock first, libvirt-sock fallback. `ConnectToURI(QEMUSession)` uses `qemu:///session`, not `qemu:///system`.

### `<backend type='passt'/>` required for `<portForward>`

libvirt ≥ 9.x `<portForward>` on `<interface type='user'>` only activates with `<backend type='passt'/>`. Emitted automatically by `renderDefaultInterface`; if you author a custom interface, include the backend line. Symptom when missing: ssh on port 2224 accepts TCP but lands in the wrong guest port. See `/charly-internals:libvirt-renderer`.

### Per-VM NVRAM required for UEFI

When `firmware: uefi-insecure` or `firmware: uefi-secure`, each VM gets its own NVRAM file under `~/.local/share/charly/vm/charly-<name>/nvram.fd`, copied from the OVMF_VARS template. Preserves UEFI variable state across reboots. See `/charly-internals:ovmf`.

### Resource sizing over package pruning (cloud_image)

Cloud-init `packages: [spice-vdagent]` pulls GTK3 + X11 (~200 MB download, ~1 GB installed). Running at 2 GiB RAM stalls cloud-init. **Check host inventory (`free -h`, `nproc`) before sizing** — give the VM what it needs (8 GiB + 4 cpus for a desktop-class workload is reasonable on a 16-core / 61 GiB host). See `/charly-vm:arch-cloud-vm` Finding C for the live-test bisect.

### Rootless podman cannot run the privileged VM builder — set `engine.rootful=sudo`

On a host whose default engine is **rootless podman**, `charly vm build` fails
inside the privileged bootstrap builder (pacstrap / debootstrap) with:

```
mount: /target/sys: permission denied.
==> ERROR: failed to setup chroot /target
```

The cause is the user NAMESPACE, not sysfs itself: `--privileged` in a rootless
container has no `CAP_SYS_ADMIN` equivalent for arbitrary bind mounts, so
pacstrap's `mount --rbind` of the locked `/sys` is denied. The remedy is one
setting — `sudo podman` runs the builder in the host namespace and bypasses it:

```bash
charly settings set engine.rootful sudo
```

Symptom without it is exactly the `mount: /target/sys: permission denied` line
above (the build gets through the image tag, then dies at the chroot). The
SDK's `buildkit/privileged_runner.go` carries this rationale in a code comment;
the rootful ↔ rootless storage split this setting also causes is documented in
the next section.

### Rootful ↔ rootless podman storage split (bootc)

Under `engine.rootful=sudo`, `charly vm build` invokes `sudo podman` which reads from `/var/lib/containers/storage`, not your rootless storage. After every `charly box build`, refresh:

```bash
podman save ghcr.io/<registry>/<image>:latest -o /tmp/img.tar
sudo podman load -i /tmp/img.tar && rm -f /tmp/img.tar
```

Symptom without refresh: `Trying to pull ... 403 Forbidden`.

### `engine.rootful=machine` path (rootless containers)

When `charly vm build` runs from inside a rootless container (no host `sudo`), the engine auto-picks `engine.rootful=machine` — spawns a podman-machine VM with its own rootful storage (`podman --connection charly-root`). Cross-storage load pattern:

```bash
podman save -o /tmp/bootc.tar ghcr.io/<registry>/<image>:latest
podman --connection charly-root load -i /tmp/bootc.tar
rm /tmp/bootc.tar
charly vm build <name> --transport containers-storage
```

`--transport containers-storage` forces `bootc install` to pull from the machine's local store.

### `charly vm destroy` — the DEPLOY name keys the domain (`--domain`) and missing targets are strict

The libvirt domain is keyed by the **DEPLOY name** — `charly-<VmDomainIdentity(deploy)>` (strip a
leading `vm:`; flatten `/` and `.` to `-`), NOT the shared `kind:vm` ENTITY (P33). A `vm:` bed named
`check-charly-vm` whose node is `vm: {from: charly-vm}` runs the domain **`charly-check-charly-vm`**,
and a sibling bed on the same entity runs its OWN distinct `charly-<bed>` domain — collision-free by
construction. A DIRECT `charly vm create/destroy <entity>` (no `--domain`) keeps `charly-<entity>`
(the direct-create deploy name IS the entity). So the deploy path tears a domain down with
`charly vm destroy <entity> --domain <deploy>` (resolve the entity spec, target `charly-<deploy>`);
manual cleanup of a legacy pre-P33 orphan (named `charly-<entity>`) uses the bare
`charly vm destroy <entity>`.

**`charly vm destroy` on a name with no domain exits non-zero with `no such VM`.** This strict
operator default prevents a mistyped entity or missing `--domain` from looking successful while the
real domain remains running. Internal reconcilers whose contract expects absence use the explicit
`--if-exists` flag; that mode succeeds silently on a missing runtime domain and still removes managed
autostart, SSH, disk (when requested), and deploy metadata.

```
$ charly vm destroy definitely-not-a-vm-xyz ; echo "exit=$?"
no such VM "charly-definitely-not-a-vm-xyz": no libvirt domain and no qemu state — nothing destroyed
exit=1
```

### `charly deploy add vm:<vm> <localpkg-candy>` exits 0 WITHOUT installing

A candy carrying a `local_pkg` block (the `charly` toolchain) needs the package
built via the `charly generate-packages` plugin (nFPM) from local source, which
only the check-bed runner does (it sets `--dev-local-pkg` automatically — see
`/charly-check:check`). A bare `charly deploy add
vm:<vm> charly` returns **rc=0** and prints:

```
guest charly absent/outdated; host charly provided at /tmp/charly-<ver> for deploy use
```

That line means the deploy **staged** the host binary into the guest for its own use — NOT
that the charly package was installed, and NOT that `charly` is on the guest's PATH. Do not read
rc=0 as "installed"; check `command -v charly` in the guest. The bed that genuinely
proves the binary-install deploy path is `check-charly-vm`.

(This also means a nested `local:` child of a `vm:` bed needs **no** charly in the guest — the
vm deploy stages what it needs. See `/charly-core:deploy` "Deploy-into nesting".)

### `charly vm stop` — authoritative backend probe + stop verification

`charly vm stop <name>` tears the domain down from whichever backend ACTUALLY holds it (probing libvirt authoritatively via the shared `vmHolder` — the same primitive `charly vm destroy` uses — regardless of the configured/default `vm.backend`) and VERIFIES the domain is no longer RUNNING before reporting success. The verify POLLS for the domain to leave `DomainRunning` (what `charly vm list -a` reports as "running") over the `StopGate` grace — the libvirt stop is an async ACPI poweroff, so the poll waits for the guest to power off rather than re-probing instantly (which would false-positive mid-shutdown). A guest that ignores ACPI / reboots-on-shutdown and is still RUNNING after the grace is a hard failure, never a silent "Stopped VM" — the error points at `charly vm stop --force` (a `destroyDomain` that forces the guest off, the same force backstop `charly vm destroy`'s `gracefulStopDomain` uses). A name with no libvirt domain and no qemu state — a genuinely-absent VM — errors "no such VM" (mirroring `charly vm destroy`), so a typo never false-succeeds. (The pre-#77 "trust the single resolved backend" arm took the qemu no-op path for a libvirt domain and falsely reported "Stopped VM" while the domain kept running; #77 closed it.)

### Host kernel/module-dir mismatch

Rolling distros after a kernel upgrade: `/lib/modules/$(uname -r)` missing, `modprobe loop` fails, VM disk build hangs. **Resolution: reboot.** Check: `ls /lib/modules/$(uname -r) >/dev/null 2>&1 && echo ok || echo "reboot needed"`.

### Disk exhaustion from `output/`

`bootc install to-disk` writes both a ~40 GiB sparse raw AND a qcow2. Failed `qemu-img convert` leaves both behind. Clean `output/raw/disk.raw` after successful qcow2 conversion; watch `df -h /` before fresh VM builds.

### `qemu-guest-agent` inactive under QEMU user-net

Expected. The agent needs a `virtio-serial` channel that charly's QEMU backend doesn't expose under user-net. Switch to libvirt backend (`charly settings set vm.backend libvirt`) to activate it. See `/charly-distros:qemu-guest-agent`.

## Cross-References

- `/charly-vm:vms-catalog` — **VM entity authoring reference** (VmSpec schema, source.kind, adopt pattern)
- `/charly-vm:arch-cloud-vm` — canonical cloud_image VM (BIOS / virtio-gpu / resource sizing / stale BOOTX64.EFI RCA)
- `/charly-internals:vm-spec` — Go type reference; validation rules; migration map
- `/charly-internals:libvirt-renderer` — RenderDomainXML + device emission + passt backend + virtio-gpu
- `/charly-internals:cloud-init-renderer` — RenderCloudInit + composeUsers + seed ISO + charly_install
- `/charly-internals:vm-deploy-target` — the external vm deploy + SSHExecutor + VmDeployState
- `/charly-internals:ovmf` — UEFI firmware path resolution; per-VM NVRAM; bios-skips-loader sentinel
- `/charly-internals:cutover-policy` — hard cutover policy
- `/charly-build:migrate` — `charly migrate` legacy conversion
- `/charly-core:deploy` — `charly deploy add vm:<name> <ref>` in-guest layer application
- `/charly-build:pull` — fetch container images into local storage (prereq for bootc VM builds)
- `/charly-build:build` — building container images before VM disk builds
- `/charly-image:layer` — `libvirt.snippets:` field in charly.yml
- `/charly-distros:cloud-init` — guest-side cloud-init package (complements host-side seed ISO rendering)
- `/charly-distros:qemu-guest-agent` — virtio-serial channel

## When to Use This Skill

**MUST be invoked** when the task involves virtual machines, charly vm commands, kind:vm entities, cloud_image vs bootc source types, libvirt/QEMU backends, BIOS vs UEFI firmware choice, or VM lifecycle management. Invoke this skill BEFORE reading source code or launching Explore agents.

**Workflow position:** Standalone workflow. VM management is separate from container lifecycle, but `charly deploy add vm:<name>` bridges into the shared InstallPlan + deploy-target machinery.

Live-deploy verification: see /charly-check:check (the 11 Testing Standards) and /charly-internals:disposable.
