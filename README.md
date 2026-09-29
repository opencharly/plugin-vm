# plugin-vm

Virtual machines for OpenCharly — the `libvirt:` check verb and the `charly vm`
lifecycle CLI.

The plugin owns the VM subsystem: the `libvirt:` live check verb (list, info,
screenshot, send-key, QMP, qemu-guest-agent, snapshots, events, and the host-side
detached framebuffer recorder `session`) plus the internal VM-resolution ops, and
`command:vm` — the `charly vm …` lifecycle CLI (build, create, start, stop,
destroy, console, ssh, snapshot, gpu, import, clone, cp-box, list). It is
**compiled-in** and dispatched in-proc, so its handlers run in charly's own
process (native stdio/TTY for `console`/`ssh`).

The handlers reach host-only mechanisms over generic seams: the config loader +
deploy-ledger read via `HostBuild("config-resolve")`, the deploy-ledger write
plugin-side via `deploykit.SaveVmDeployState`/`RemoveVmDeployEntry`, the VM-disk
build engine plugin-side, `egress` via `verb:egress`, `preempt` via
`verb:arbiter`, GPU via `verb:gpu`. All of it is backed by `go-libvirt` +
`kata-containers/govmm` + `libvirt.org/go/libvirtxml`.

## What it provides

| Capability | Surface |
|---|---|
| `verb:libvirt` | the `libvirt:` check verb (list, info, screenshot, send-key, QMP, guest agent, snapshots, events, session) + the internal VM-resolution ops |
| `command:vm` | the `charly vm …` lifecycle CLI |

## How to use it

```bash
charly vm create <name> ...
charly vm list
charly vm console <name>
charly vm ssh <name>
```

Author the `libvirt:` verb in a check bed:

```yaml
- check: the libvirt domains are listed
  libvirt: list
  context: [runtime]
```

## Layout

- `candy/plugin-vm/` — the plugin module: `vm_*.go` (the lifecycle engines),
  `libvirt*.go` (the verb), `command.go` (the CLI), `schema/vm.cue`,
  `params/cue_types_gen.go`, `cmd/serve/main.go`.
- `charly.yml` — the root project manifest (`discover: candy`).
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Platforms

Builds for `linux/amd64`, `linux/arm64` and `linux/arm/v7`. The armv7 target
works because of the sdk's 32-bit fix
([opencharly/sdk#263](https://github.com/opencharly/sdk/pull/263), issue
[#262](https://github.com/opencharly/sdk/issues/262)) — `charly`'s loader
host-builds this plugin with `CGO_ENABLED=0`, so the artifact is a static binary,
which is what an armv7 appliance without a glibc toolchain (such as a JetKVM's
uClibc userland) runs.

## Related

- Owning skill: `/charly-vm:vm` — the VM lifecycle surface, `kind:vm` entities,
  libvirt/QEMU backends, and VM operations this plugin owns.
- `/charly-internals:plugin` — the plugin/provider model.
- [`opencharly/charly`](https://github.com/opencharly/charly) — the charly CLI.
