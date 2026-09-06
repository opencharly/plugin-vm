package vm

import (
	"fmt"
)

// vm_clone_cmd.go — Kong subcommand wiring for `charly vm clone`. The
// command is a thin frontend over BuildClone in vm_clone.go: it
// resolves the source-vm@snapshot reference, persists a kind:vm
// declaration to vm.yml, and calls the standard build/create flow.

// VmCloneCmd implements `charly vm clone <new> --from <src>[@<snap>]`.
type VmCloneCmd struct {
	// Name is the new VM name (kind:vm entity key).
	Name string `arg:"" help:"New VM name"`

	// From is the source VM, optionally with @snapshot. Forms:
	//   --from arch              → clone from arch's current state (auto-snapshot)
	//   --from arch@baseline     → clone from arch's "baseline" snapshot
	From string `name:"from" required:"" help:"Source VM, optionally @snapshot (e.g. arch@baseline)"`

	// CloudInitClean injects cloud-init clean --machine-id into the
	// clone's user-data. Default true for ad-hoc clones (so two clones
	// don't collide on machine-id).
	CloudInitClean bool `name:"cloud-init-clean" default:"true" help:"Regenerate machine-id and SSH host keys on first boot"`

	// Build, when true, also runs `charly vm build` after writing vm.yml.
	Build bool `name:"build" default:"true" help:"After writing vm.yml, run charly vm build to materialize the clone disk"`
}

// Run executes `charly vm clone`. RETIRED (Cutover A addendum Phase 3): the command
// persisted a source.kind: clone ENTITY — the entity arm the retirement removes. The
// clone is now expressed on the DEPLOY: author `vm: <name>: {from: <src>:<snapshot>}`
// (the loader splits the tag into from + from_snapshot) and run the deployment; or build
// a one-off self-clone with `charly vm build <entity> --from-snapshot <tag>`.
func (c *VmCloneCmd) Run() error {
	return fmt.Errorf("charly vm clone is retired: a clone is expressed on the DEPLOY as from: <src>:<snapshot> (author `vm: <name>: from: <src>:<snapshot>` in a deploy node), or build a one-off self-clone with `charly vm build <entity> --from-snapshot <tag>`")
}
