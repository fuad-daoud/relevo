package proc

import (
	"strconv"
	"strings"
)

// gitNoFsmonitorEnv returns the entries that disable git's fsmonitor for a
// spawn, so a builder's git commands never leave an fsmonitor--daemon behind to
// keep its round's scope alive. n continues the count the child would otherwise
// have -- extra first, else parent, else 0 -- so git's existing entries stay
// valid and this one wins; the caller denies the parent's GIT_CONFIG_COUNT. A
// non-numeric count is treated as 0, and the inputs are never mutated.
func gitNoFsmonitorEnv(parent, extra []string) []string {
	n := 0
	if v, ok := envLookup(extra, "GIT_CONFIG_COUNT"); ok {
		n, _ = strconv.Atoi(v) // a non-numeric count is 0
	} else if v, ok := envLookup(parent, "GIT_CONFIG_COUNT"); ok {
		n, _ = strconv.Atoi(v)
	}
	idx := strconv.Itoa(n)
	return []string{
		"GIT_CONFIG_KEY_" + idx + "=core.fsmonitor",
		"GIT_CONFIG_VALUE_" + idx + "=false",
		"GIT_CONFIG_COUNT=" + strconv.Itoa(n+1),
	}
}

// envLookup returns the value of name in env and whether name is set at all,
// matching the same way ChildEnv's deny list does: an entry "NAME=..." or
// exactly "NAME" (whose value is ""). The last entry wins, as os/exec resolves
// a duplicated name.
func envLookup(env []string, name string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		n, v, has := strings.Cut(e, "=")
		if n != name {
			continue
		}
		if has {
			value = v
		} else {
			value = ""
		}
		found = true
	}
	return value, found
}
