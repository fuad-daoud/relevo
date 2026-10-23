package proc

import (
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TmpDirEnv names the variables that decide where a child puts its temporary
// files. relevo points them at the process's own directory, or at the runner's
// default when the process names none, so parallel builds keep their scratch
// off a shared system temp instead of filling a shared quota. tmpDirEnv states
// which source wins for each name.
var TmpDirEnv = []string{"TMPDIR", "GOTMPDIR"}

// DeniedEnv names the variables relevo never passes to a child it spawns. They
// are relevo's own -- a secret and its identities -- not the harness's, which
// needs its provider credentials; a user who wants a builder to hold one sets
// it in the harness's own config. The identity names are denied so a spawned
// child cannot inherit a MasterMind, planner or runner it is not: a round's own
// marker is appended after this filter and therefore survives, while a parent's
// stale one cannot shadow it.
var DeniedEnv = []string{"TYPESAFE_API_KEY", "RELEVO_MASTERMIND", "RELEVO_PLANNER", "RELEVO_RUNNER", "RELEVO_BASE"}

// ChildEnv returns parent with every denied name removed, then extra appended
// verbatim. A name matches as "NAME=..." or exactly "NAME"; extra is not
// filtered, since it is relevo's own and may set a denied name deliberately.
func ChildEnv(parent, deny, extra []string) []string {
	denied := make(map[string]struct{}, len(deny))
	for _, d := range deny {
		denied[d] = struct{}{}
	}

	out := make([]string, 0, len(parent)+len(extra))
	for _, e := range parent {
		name, _, _ := strings.Cut(e, "=")
		if _, ok := denied[name]; ok {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}

// goMaxProcsEnv returns the one GOMAXPROCS entry a scoped child needs, or nil: a
// nil scope, a scope that limits no CPUs, or an extra that already sets it. An
// inherited parent GOMAXPROCS does not stop it -- the scope's limit is the
// operator's explicit choice -- and Start denies the parent's entry.
func goMaxProcsEnv(parent, extra []string, scope *spawn.ScopeSpec) []string {
	if scope == nil {
		return nil
	}
	n, ok := spawn.GoMaxProcsFor(*scope)
	if !ok {
		return nil
	}
	if hasEnvName(extra, "GOMAXPROCS") {
		return nil
	}
	return []string{"GOMAXPROCS=" + strconv.Itoa(n)}
}

// tmpDirEnv returns the temp-dir entries a child needs and the names those
// entries replace in the parent. Each name is decided on its own: an extra
// entry wins, then the spec's own directory, then a non-empty inherited value,
// and only a name nothing of these sets takes runnerDir. specDir is chosen for
// this process alone, so it outranks the environment the process was launched
// in; an empty spec and runner default nothing, so a caller that names neither
// keeps its children's environment untouched.
//
// The entry is appended after the parent's and the name is denied there, so the
// child's only copy is the default: a parent carrying an empty "TMPDIR=" would
// otherwise reach the child first and win a first-match reader.
func tmpDirEnv(parent, extra []string, specDir, runnerDir string) (add, deny []string) {
	for _, name := range TmpDirEnv {
		if hasEnvName(extra, name) {
			continue
		}
		dir := specDir
		if dir == "" {
			if v, ok := envLookup(parent, name); ok && v != "" {
				continue
			}
			dir = runnerDir
		}
		if dir == "" {
			continue
		}
		add = append(add, name+"="+dir)
		deny = append(deny, name)
	}
	return add, deny
}

// hasEnvName reports whether env carries name, matching as ChildEnv's deny does.
func hasEnvName(env []string, name string) bool {
	for _, e := range env {
		if n, _, _ := strings.Cut(e, "="); n == name {
			return true
		}
	}
	return false
}
