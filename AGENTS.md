# AGENTS.md — plugin-vm

Standalone plugin repo for the VM subsystem (`verb:libvirt` + `command:vm`). The
plugin is a Go module at `candy/plugin-vm/` (module path
`github.com/opencharly/plugin-vm/candy/plugin-vm`); the root `charly.yml` only
declares `discover: candy` so the repo is a project and its candy is scanned.

Canonical files:

- `candy/plugin-vm/charly.yml` — the `plugin-vm:` candy entity (`plugin:` block,
  `plan:` checks).
- `candy/plugin-vm/vm_*.go` — the VM lifecycle engines (build/create/start/stop/
  destroy/snapshot/gpu/import/clone/…).
- `candy/plugin-vm/libvirt*.go` — the `libvirt:` verb + libvirt plumbing.
- `candy/plugin-vm/command.go` — the `charly vm` Kong grammar.
- `candy/plugin-vm/schema/vm.cue` — the self-contained input schema.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.
- `README.md` — user overview only; never agent guidance.

## Load these skills first (R0)

- `/charly-vm:vm` — the VM lifecycle surface, `kind:vm` entities, libvirt/QEMU
  backends, and VM operations this plugin owns.
- `/charly-internals:plugin` — the plugin authoring reference: the `plugin:`
  block, the unified Provider model, the per-plugin CUE-schema contract,
  placement. Load before touching the provider or schema.
- `/charly-internals:vm-spec` — the `VmSpec`/libvirt/cloud-init/OVMF internals.
- `/charly-internals:git-workflow` — before any git/PR action.

## Build / validate / test

- `go build ./...` in `candy/plugin-vm/` — compile the plugin module.
- `go test ./...` in `candy/plugin-vm/` — the plugin's extensive Go test suite.
- `charly box validate` at the repo root — the structural check (the candy +
  `plugin:` block, CUE schema).
- The merge gate is the **org-wide** `charly/pr-validator` (required check
  `validate / validate`, defined in `opencharly/.github`); this repo has **no**
  per-repo candy gate.
- R10 witness: the `plan:` checks run `libvirt: list` + the detached `session`
  recorder against a live VM deployment.

## Modify this repo

- Edit the `plugin-vm:` candy entity, the Go source, and `schema/vm.cue`
  **together** — the schema is the single source for the `params/` struct, so a
  field change not mirrored in the schema desyncs the generated types.
- The plugin is **compiled-in**; its handlers run in charly's own process (native
  stdio/TTY) and reach host-only mechanisms over generic seams — do not add a
  bespoke host RPC.

## Landing

- PR-only. Every change lands through a pull request; the org-required
  `charly/pr-validator` validates the diff and body and arms native auto-merge on
  PASS. Direct pushes to `main` are blocked.
- History lives in `CHANGELOG/` (written by `tag-on-merge` at merge time); the PR
  body IS the changelog.
- The authoritative rulebook is the umbrella `AGENTS.md` in
  `opencharly/opencharly` and `charly/AGENTS.md` in the charly repo. Do not
  restate its rules here.
