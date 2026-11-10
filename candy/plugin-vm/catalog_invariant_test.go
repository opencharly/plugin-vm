package vm

// catalog_invariant_test.go pins the invariant the jetkvm, cua, punktfunk, gh, omarchy,
// herdr, spice, desktop-kind and appium siblings keep and this plugin needs for the same
// reason: every word the SCHEMA advertises must be reachable, and every word the plugin
// routes must be in the schema. Without it a word can sit in the catalog as a phantom
// (advertised but unimplemented): the schema validates the authored `libvirt: …` step, and
// the step then fails only at dispatch with an unknown-method error, so the catalog and the
// skill that documents it mislead the agent that dispatches on it.
//
// THIS repo routes `method` in TWO places, and the invariant has to read both or it is
// wrong in the most dangerous direction:
//
//   - provider.go special-cases `session` BEFORE dispatch (the detached recorder holds the
//     libvirt RPC, so the provider never dials and dispatchLibvirtVerb is never reached);
//   - every other method is an entry in libvirt_methods.go's `libvirtMethods` map, which
//     dispatchLibvirtVerb looks up and turns into an in-process CLI invocation. A test that
//     looked for a `switch method` would find NO arms at all and report all 27 methods as
//     phantoms.
//
// It also pins the map's own internals, which are data no type checks:
//
//   - each entry's `Path` drives the CLI subcommand, so a copy-pasted path dispatches a
//     DIFFERENT method than the key it is filed under, silently;
//   - each entry's `Required` names plugin-INPUT keys (or the one documented shared
//     `target`), and a name no field carries makes the method permanently unrunnable;
//   - session_method.go's `switch in.Action` IS `#LibvirtVerbInput.action`'s router, so the
//     `_action` table and that switch must agree in both directions.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"cuelang.org/go/cue"
)

var (
	runSessionFunc = regexp.MustCompile(`(?s)func runSession\(.*?\n\}`)
	caseArm        = regexp.MustCompile(`(?m)^[ \t]*case[ \t]+((?:"(?:[^"\\]|\\.)*"[ \t]*,?[ \t]*)+):`)
	quotedWord     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	// provider.go routes one method before dispatch, as a plain comparison.
	preDispatch = regexp.MustCompile(`method == "([a-z][a-z0-9/-]*)"`)
	// libvirtMethods is a Go map literal of method -> kit.MethodSpec, one entry per line.
	methodsMap   = regexp.MustCompile(`(?s)var libvirtMethods = map\[string\]kit\.MethodSpec\{(.*?)\n\}`)
	methodEntry  = regexp.MustCompile(`(?m)^\t"([a-z][a-z0-9/-]*)":\s*\{(.*)\},?\s*$`)
	pathField    = regexp.MustCompile(`Path: \[\]string\{([^}]*)\}`)
	requireField = regexp.MustCompile(`Required: \[\]string\{([^}]*)\}`)
)

func readSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// schemaMethods returns the sorted method names the SERVED #LibvirtVerbInput.method enum
// accepts. It reads the schema through the CUE API rather than its text: the enum is now
// §10.1 S3, and a text parse of the union would find ZERO methods and leave every invariant
// here passing vacuously.
func schemaMethods(t *testing.T) []string {
	t.Helper()
	return fieldWordList(t, servedSchema(t), "LibvirtVerbInput", "method")
}

// schemaActions returns the sorted words the SERVED #LibvirtVerbInput.action enum accepts.
func schemaActions(t *testing.T) []string {
	t.Helper()
	return fieldWordList(t, servedSchema(t), "LibvirtVerbInput", "action")
}

// methodSpec is one parsed entry of the libvirtMethods map.
type methodSpec struct {
	key      string
	path     []string
	required []string
}

// methodSpecs parses the dispatcher's allowlist. It fails loud when the anchor moved rather
// than reading an empty map and reporting every advertised method as a phantom.
func methodSpecs(t *testing.T) []methodSpec {
	t.Helper()
	src := readSource(t, "libvirt_methods.go")
	block := methodsMap.FindStringSubmatch(src)
	if block == nil {
		t.Fatal("could not locate the libvirtMethods map literal in libvirt_methods.go — if it moved, this test must move with it, not silently pass")
	}
	var out []methodSpec
	for _, m := range methodEntry.FindAllStringSubmatch(block[1], -1) {
		spec := methodSpec{key: m[1]}
		if p := pathField.FindStringSubmatch(m[2]); p != nil {
			for _, q := range quotedWord.FindAllStringSubmatch(p[1], -1) {
				spec.path = append(spec.path, q[1])
			}
		}
		if r := requireField.FindStringSubmatch(m[2]); r != nil {
			for _, q := range quotedWord.FindAllStringSubmatch(r[1], -1) {
				spec.required = append(spec.required, q[1])
			}
		}
		out = append(out, spec)
	}
	if len(out) == 0 {
		t.Fatal("the libvirtMethods map parsed to zero entries — this check would pass over nothing")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// routedMethods returns every method word the plugin routes: the allowlist's keys plus
// provider.go's pre-dispatch comparison.
func routedMethods(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range methodSpecs(t) {
		seen[s.key] = true
	}
	prov := readSource(t, "provider.go")
	pre := preDispatch.FindAllStringSubmatch(prov, -1)
	if len(pre) == 0 {
		t.Fatal("could not locate the pre-dispatch `method == \"…\"` comparison in provider.go — if it moved, this test must move with it, not silently pass")
	}
	for _, m := range pre {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// TestCatalogHasNoPhantomMethods asserts every schema method is routed — no word is
// advertised that the plugin cannot act on.
func TestCatalogHasNoPhantomMethods(t *testing.T) {
	reachable := map[string]bool{}
	for _, m := range routedMethods(t) {
		reachable[m] = true
	}
	if len(reachable) == 0 {
		t.Fatal("the dispatch surface is empty — the invariant would pass over nothing")
	}
	for _, m := range schemaMethods(t) {
		if !reachable[m] {
			t.Errorf("schema method %q is a PHANTOM: neither provider.go's pre-dispatch comparison nor the libvirtMethods allowlist routes it — add an implementation or remove it from the table", m)
		}
	}
}

// TestDispatchHasNoPhantomArms is the other direction: an arm for a word the schema no
// longer accepts is dead code, and it hides a rename that only half landed.
func TestDispatchHasNoPhantomArms(t *testing.T) {
	advertised := map[string]bool{}
	for _, m := range schemaMethods(t) {
		advertised[m] = true
	}
	for _, m := range routedMethods(t) {
		if !advertised[m] {
			t.Errorf("the plugin routes %q, which the served #LibvirtVerbInput.method enum does NOT accept — the arm is unreachable", m)
		}
	}
}

// TestEverySchemaMethodIsRouted is the two-way set equality in one assertion.
func TestEverySchemaMethodIsRouted(t *testing.T) {
	schema := schemaMethods(t)
	routed := routedMethods(t)
	if len(schema) != len(routed) {
		t.Fatalf("served enum has %d methods but the plugin routes %d — the catalog and the dispatch have drifted\n schema=%v\n routed=%v",
			len(schema), len(routed), schema, routed)
	}
	for i := range schema {
		if schema[i] != routed[i] {
			t.Errorf("served method %q has no matching routed arm (routed=%v)", schema[i], routed)
			break
		}
	}
}

// TestMethodSpecPathsDispatchTheirOwnMethod pins the map's PATH data: dispatchLibvirtVerb
// drops the leading "libvirt" and runs the rest as the Kong subcommand path, so the path
// tail must BE the key. A copy-pasted entry whose path still names its neighbour's
// subcommand runs the wrong verb and reports the neighbour's output under this method's
// name — no type, no test, and no dispatch error can see it.
func TestMethodSpecPathsDispatchTheirOwnMethod(t *testing.T) {
	checked := 0
	for _, s := range methodSpecs(t) {
		if len(s.path) == 0 {
			t.Errorf("libvirtMethods[%q] has no Path — dispatchLibvirtVerb would run the bare `libvirt` CLI", s.key)
			continue
		}
		if s.path[0] != "libvirt" {
			t.Errorf("libvirtMethods[%q].Path starts with %q, want %q — dispatchLibvirtVerb drops the FIRST element", s.key, s.path[0], "libvirt")
			continue
		}
		checked++
		if got := strings.Join(s.path[1:], "/"); got != s.key {
			t.Errorf("libvirtMethods[%q].Path dispatches %q — the entry runs a DIFFERENT subcommand than the method it is filed under", s.key, got)
		}
	}
	if checked == 0 {
		t.Fatal("no entry carried a path to check — this invariant would pass over nothing")
	}
}

// TestMethodSpecRequiredNamesAreInputFields pins the map's REQUIRED data. The names are
// plugin-INPUT keys since the schema-compaction cutover — one exception is documented in the
// schema itself: the snapshot methods require the SHARED step-level `target:`, which is a
// core #Op field and not part of #LibvirtVerbInput. Anything else that names no field can
// never be satisfied, so the method is permanently unrunnable with a message naming a key no
// author can write.
func TestMethodSpecRequiredNamesAreInputFields(t *testing.T) {
	input := servedSchema(t).LookupPath(cue.ParsePath("#LibvirtVerbInput"))
	if err := input.Err(); err != nil {
		t.Fatalf("#LibvirtVerbInput does not resolve in the served schema: %v", err)
	}
	fields := map[string]bool{}
	it, err := input.Fields(cue.Optional(true))
	if err != nil {
		t.Fatalf("#LibvirtVerbInput has no fields: %v", err)
	}
	for it.Next() {
		// An OPTIONAL field's selector string carries the constraint marker (`fps?`) —
		// `cue.Optional(true)` is what makes those fields visible at all, and the author
		// writes the bare name.
		fields[strings.TrimSuffix(it.Selector().String(), "?")] = true
	}
	// CALIBRATION: the reader must see fields the def certainly carries, so a formatting
	// regression fails HERE instead of reporting every required name as unknown.
	if !fields["method"] || !fields["command"] {
		t.Fatalf("#LibvirtVerbInput's field reader does not see its own `method`/`command` fields (read %d) — the reader is broken, not the map", len(fields))
	}
	const sharedTarget = "target"
	checked := 0
	for _, s := range methodSpecs(t) {
		for _, name := range s.required {
			checked++
			if name == sharedTarget {
				continue // the documented SHARED step-level #Op field
			}
			if !fields[name] {
				t.Errorf("libvirtMethods[%q] requires the input field %q, which #LibvirtVerbInput does not carry (and which is not the shared step-level %q) — the method can never satisfy the check", s.key, name, sharedTarget)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no entry carried a required name to check — this invariant would pass over nothing")
	}
}

// TestSessionActionsAreSchemaActions pins the `_action` table against its ROUTER:
// session_method.go's `switch in.Action`. A merged or mistyped set falls through to the
// method's own error at dispatch instead of failing the schema.
func TestSessionActionsAreSchemaActions(t *testing.T) {
	src := readSource(t, "session_method.go")
	fn := runSessionFunc.FindString(src)
	if fn == "" {
		t.Fatal("could not locate runSession in session_method.go")
	}
	routed := map[string]bool{}
	for _, arm := range caseArm.FindAllStringSubmatch(fn, -1) {
		for _, q := range quotedWord.FindAllStringSubmatch(arm[1], -1) {
			routed[q[1]] = true
		}
	}
	if len(routed) == 0 {
		t.Fatal("runSession routes no action — this check would pass over nothing")
	}
	advertised := map[string]bool{}
	for _, a := range schemaActions(t) {
		advertised[a] = true
	}
	for a := range routed {
		if !advertised[a] {
			t.Errorf("runSession routes the action %q, which the served #LibvirtVerbInput.action enum does NOT accept — the arm is unreachable", a)
		}
	}
	for a := range advertised {
		if !routed[a] {
			t.Errorf("#LibvirtVerbInput.action accepts %q but runSession has no arm for it — the step validates and then fails at dispatch", a)
		}
	}
}
