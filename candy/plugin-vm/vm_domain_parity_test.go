package vm

import (
	"reflect"
	"strings"
	"testing"
)

// Every verb that addresses a RUNNING guest must accept --domain.
//
// This is a parity guard, not a case list, and it exists because the gap it closes was
// found the expensive way: mid-investigation, `charly vm ssh <entity> --domain <bed>` — the
// form every other verb accepts — failed with "unknown flag --domain", and the only way in
// was to pass the DOMAIN in the box slot and rely on the alias happening to be keyed that
// way. That works by coincidence, is documented nowhere, and breaks the moment anything
// resolves the entity from that argument.
//
// A deploy's libvirt domain, disk overlay, per-domain state and ssh alias are all keyed by
// the DEPLOY name, not the kind:vm entity — sibling beds share one entity and must get
// distinct domains. So a verb without --domain cannot address a bed's guest at all, which
// is exactly when an operator needs it.
//
// Listing the verbs by hand here would reproduce the original defect: a new verb would be
// added and the list would not be. Instead the guard REFLECTS over VmCmd, and a verb is
// exempt only by appearing in the table below WITH a reason.
func TestEveryGuestAddressingVerbAcceptsDomain(t *testing.T) {
	// Verbs that legitimately have no --domain, each with the reason it does not.
	exempt := map[string]string{
		"Build": "operates on the ENTITY's shared build output, before any domain exists",
		"Clone": "writes a new kind:vm declaration; it names entities, not domains",
		"Gpu":   "inspects the HOST's VFIO state; no guest is involved",
		"List":  "enumerates every domain, so selecting one would defeat it",
		"Import": "adopts an EXISTING libvirt domain by its own name, which is the " +
			"positional argument — a second name for the same thing would be ambiguous",
		"Seed":     "a subcommand group; --domain lives on its leaves (ls, cat)",
		"Snapshot": "a subcommand group; --domain lives on its leaves (create, list, delete, revert, promote)",
	}

	cmd := reflect.TypeOf(VmCmd{})
	for i := 0; i < cmd.NumField(); i++ {
		verb := cmd.Field(i)
		if reason, ok := exempt[verb.Name]; ok {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is exempt with no reason given", verb.Name)
			}
			continue
		}
		if !hasDomainFlag(verb.Type) {
			t.Errorf("`vm %s` does not accept --domain, so it cannot address a DEPLOY's guest "+
				"(a bed's domain is the BED name, not the entity). Add a Domain field and route "+
				"it through domainOr(), or add %s to the exempt table WITH the reason.",
				strings.ToLower(verb.Name), verb.Name)
		}
	}
}

// hasDomainFlag reports whether a command struct carries a `--domain` kong flag, looking one
// level into subcommand groups so a group whose leaves all carry it counts.
func hasDomainFlag(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "Domain" && strings.Contains(string(f.Tag), `name:"domain"`) {
			return true
		}
	}
	return false
}

// The exempt table must not drift into a place to hide a real gap: every name in it has to
// still be a verb on VmCmd.
func TestDomainExemptionsNameRealVerbs(t *testing.T) {
	cmd := reflect.TypeOf(VmCmd{})
	present := map[string]bool{}
	for i := 0; i < cmd.NumField(); i++ {
		present[cmd.Field(i).Name] = true
	}
	for _, name := range []string{"Build", "Clone", "Gpu", "List", "Import", "Seed", "Snapshot"} {
		if !present[name] {
			t.Errorf("the --domain exempt table names %q, which is no longer a vm verb; "+
				"a stale exemption silently excuses the next verb that reuses the name", name)
		}
	}
}

// The two GROUP exemptions above are delegations, not holes: assert every leaf carries the
// flag its group was excused for.
func TestSubcommandGroupLeavesAcceptDomain(t *testing.T) {
	groups := map[string]reflect.Type{
		"seed":     reflect.TypeOf(VmSeedCmd{}),
		"snapshot": reflect.TypeOf(VmSnapshotCmd{}),
	}
	for verb, grp := range groups {
		for i := 0; i < grp.NumField(); i++ {
			leaf := grp.Field(i)
			if !hasDomainFlag(leaf.Type) {
				t.Errorf("`vm %s %s` does not accept --domain, so it addresses the ENTITY when "+
					"asked about a DEPLOY — for seed those are different files (the per-domain "+
					"ssh key), and for snapshot a different libvirt domain entirely",
					verb, strings.ToLower(leaf.Name))
			}
		}
	}
}
