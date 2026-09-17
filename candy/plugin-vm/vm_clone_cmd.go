package vm

import (
	"fmt"
)

// vm_clone_cmd.go — Kong subcommand wiring for `charly vm clone`.
//
// RETIRED (Cutover A addendum Phase 3): the command used to persist a
// `source.kind: clone` ENTITY, the entity arm that retirement removed. The
// verb is kept ONLY as a hard error pointing at the two supported spellings:
// author the clone on the DEPLOY (`vm: <name>: {from: <src>:<snapshot>}` — the
// loader splits the tag into from + from_snapshot), or build a one-off
// self-clone with `charly vm build <entity> --from-snapshot <tag>`.

// VmCloneCmd implements the retired `charly vm clone` verb. Its flags are kept
// so the parser still recognizes the invocation and the error names the exact
// replacement; the command performs no work.
type VmCloneCmd struct {
	// Name is the (ignored) entity name the caller asked to clone.
	Name string `arg:"" help:"(retired) New VM name"`

	// From is the (ignored) source reference.
	From string `name:"from" required:"" help:"(retired) source VM, optionally @snapshot"`

	// CloudInitClean is ignored (retirement left the flag for parse compatibility).
	CloudInitClean bool `name:"cloud-init-clean" default:"true" hidden:"" help:"(retired)"`

	// Build is ignored (retirement left the flag for parse compatibility).
	Build bool `name:"build" default:"true" hidden:"" help:"(retired)"`
}

// Run executes `charly vm clone`. RETIRED (Cutover A addendum Phase 3): the command
// persisted a source.kind: clone ENTITY — the entity arm the retirement removes. The
// clone is now expressed on the DEPLOY: author `vm: <name>: {from: <src>:<snapshot>}`
// (the loader splits the tag into from + from_snapshot) and run the deployment; or build
// a one-off self-clone with `charly vm build <entity> --from-snapshot <tag>`.
func (c *VmCloneCmd) Run() error {
	return fmt.Errorf("charly vm clone is retired: a clone is expressed on the DEPLOY as from: <src>:<snapshot> (author `vm: <name>: from: <src>:<snapshot>` in a deploy node), or build a one-off self-clone with `charly vm build <entity> --from-snapshot <tag>`")
}
