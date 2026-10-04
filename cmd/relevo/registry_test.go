package main

import (
	"flag"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// dispatcher is one parent verb and the switch that dispatches its children:
// parent is "" for the top-level run function, and fn is the function whose
// case labels are the child names.
type dispatcher struct {
	parent string
	file   string
	fn     string
}

// dispatchers lists every function whose case labels are verb names. The walk
// reads them from the source, so a new case the registry does not describe
// fails the test whichever way the drift runs.
var dispatchers = []dispatcher{
	{"", "main.go", "run"},
	{"board", "board.go", "cmdBoard"},
	{"config", "config.go", "cmdConfig"},
	{"config secret", "config_server.go", "configSecret"},
	{"config server", "config_server.go", "configServer"},
	{"config workflow", "config_workflow.go", "configWorkflow"},
	{"db", "db_query.go", "cmdDB"},
	{"db sync", "db_sync.go", "cmdDBSync"},
	{"mastermind", "mastermind.go", "cmdMasterMind"},
	{"serve", "serve.go", "cmdServe"},
}

// removedCases are the case labels that deliberately have no entry: the verbs
// whose arms name a replacement instead of running something, plus ask, whose
// arm says the same. The set is test-local because the code no longer carries
// a map for them.
var removedCases = map[string]bool{
	"ask":               true,
	"config roles-init": true,
	"mastermind prune":  true,
	"serve log":         true,
	"serve show":        true,
	"serve tab":         true,
	"serve gates":       true,
	"serve available":   true,
	"serve unavailable": true,
}

// funcBody returns the source text of the named top-level function in file.
// A top-level function ends at a line that is exactly "}".
func funcBody(t *testing.T, file, fn string) string {
	t.Helper()

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	lines := strings.Split(string(src), "\n")
	decl := regexp.MustCompile(`^func ` + regexp.QuoteMeta(fn) + `\(`)
	start := -1
	for i, line := range lines {
		if decl.MatchString(line) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s: no func %s", file, fn)
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("%s: func %s has no closing brace at column 0", file, fn)
	return ""
}

var (
	caseLine  = regexp.MustCompile(`(?m)^\tcase (.+):$`)
	caseValue = regexp.MustCompile(`"([^"]*)"`)
)

// caseLabels returns every string in a case label of body. A dispatcher with
// no case label at all is a failure: it means the function was renamed or the
// switch moved, and the walk would silently check nothing.
func caseLabels(t *testing.T, file, fn, body string) []string {
	t.Helper()

	var labels []string
	for _, m := range caseLine.FindAllStringSubmatch(body, -1) {
		for _, v := range caseValue.FindAllStringSubmatch(m[1], -1) {
			labels = append(labels, v[1])
		}
	}
	if len(labels) == 0 {
		t.Fatalf("%s: func %s matches no case labels", file, fn)
	}
	return labels
}

// activeLabels is a dispatcher's case labels with the spellings that name no
// verb removed: dash aliases (-h/--help/-v/--version), a child dispatcher's
// own help arm, and the removed cases.
func activeLabels(t *testing.T, d dispatcher) []string {
	t.Helper()

	var out []string
	for _, label := range caseLabels(t, d.file, d.fn, funcBody(t, d.file, d.fn)) {
		if strings.HasPrefix(label, "-") {
			continue
		}
		if label == "help" && d.parent != "" {
			continue
		}
		name := label
		if d.parent != "" {
			name = d.parent + " " + label
		}
		if removedCases[name] {
			continue
		}
		out = append(out, label)
	}
	return out
}

// splitVerb splits a registry name at its last space: "config server add"
// belongs to parent "config server" and label "add". A top-level name has no
// parent.
func splitVerb(name string) (parent, label string) {
	if i := strings.LastIndex(name, " "); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// TestRegistryMatchesDispatchersBothWays walks every dispatcher's case labels
// and requires an entry for each, and a case label for each entry's tail. The
// parent in an entry's name must itself be an entry.
func TestRegistryMatchesDispatchersBothWays(t *testing.T) {
	names := map[string]bool{}
	for _, e := range registry {
		names[e.Name] = true
	}

	labelsByParent := map[string]map[string]bool{}
	for _, d := range dispatchers {
		labels := map[string]bool{}
		for _, label := range activeLabels(t, d) {
			labels[label] = true
			name := label
			if d.parent != "" {
				name = d.parent + " " + label
			}
			if !names[name] {
				t.Errorf("%s: case %q has no registry entry %q", d.fn, label, name)
			}
		}
		labelsByParent[d.parent] = labels
	}

	for _, e := range registry {
		parent, label := splitVerb(e.Name)
		if !labelsByParent[parent][label] {
			t.Errorf("entry %q: %q is not a case label of %q", e.Name, label, parent)
		}
	}
}

// TestRegistryFlagsMatchFlagSets requires exactly one installer per entry, and
// that installing one declares exactly the flags the registry lists.
func TestRegistryFlagsMatchFlagSets(t *testing.T) {
	if len(verbFlagSets) != len(registry) {
		t.Errorf("verbFlagSets has %d installers for %d entries", len(verbFlagSets), len(registry))
	}

	for _, e := range registry {
		install, ok := verbFlagSets[e.Name]
		if !ok {
			t.Errorf("entry %q has no installer", e.Name)
			continue
		}
		fs := flag.NewFlagSet(e.Name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		install(fs)

		var got []string
		fs.VisitAll(func(f *flag.Flag) { got = append(got, f.Name) })
		want := make([]string, 0, len(e.Flags))
		for _, name := range e.Flags {
			want = append(want, strings.TrimPrefix(name, "--"))
		}
		if !slices.Equal(got, want) {
			t.Errorf("entry %q: installer declares %v, registry says %v", e.Name, got, want)
		}
	}

	for name := range verbFlagSets {
		if _, ok := registryEntry(name); !ok {
			t.Errorf("installer %q has no registry entry", name)
		}
	}
}

// TestRegistryShape pins the table's own shape: names unique and in order,
// flags sorted and unique, every exit plausible (wait keeps its protocol
// codes), and every named error code catalogued.
func TestRegistryShape(t *testing.T) {
	names := make([]string, 0, len(registry))
	for _, e := range registry {
		names = append(names, e.Name)

		if e.Summary == "" {
			t.Errorf("entry %q has no summary", e.Name)
		}
		if !sort.StringsAreSorted(e.Flags) {
			t.Errorf("entry %q: flags %v are not sorted", e.Name, e.Flags)
		}
		if len(slices.Compact(slices.Clone(e.Flags))) != len(e.Flags) {
			t.Errorf("entry %q: flags %v repeat", e.Name, e.Flags)
		}
		for _, f := range e.Flags {
			if !strings.HasPrefix(f, "--") {
				t.Errorf("entry %q: flag %q is not spelled with --", e.Name, f)
			}
		}

		if e.Name == "wait" {
			if !slices.Equal(e.Exit, []int{0, 2, 3, 4, 5, 6, 124}) {
				t.Errorf("wait: exit = %v, want its protocol codes 0/2/3/4/5/6/124", e.Exit)
			}
		} else {
			for _, code := range e.Exit {
				if code != 0 && code != 1 && code != 2 {
					t.Errorf("entry %q: exit %d is outside {0,1,2}", e.Name, code)
				}
			}
		}

		for _, code := range e.Errors {
			if _, ok := catalog[errorCode(code)]; !ok {
				t.Errorf("entry %q names code %q, which is not in the catalog", e.Name, code)
			}
		}
	}

	if !sort.StringsAreSorted(names) {
		t.Errorf("registry is not in name order: %v", names)
	}
	if len(slices.Compact(slices.Clone(names))) != len(names) {
		t.Errorf("registry repeats a name: %v", names)
	}
}
