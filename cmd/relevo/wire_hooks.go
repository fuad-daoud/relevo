package main

import (
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/hooks"
)

// resolveHooksConfig builds the hook dispatcher environment: the hooks section
// the config store loaded, and the run log its runs are recorded in.
// log is the machine database's kv log; a caller with no database open
// (preflight, check, serve's admin paths) passes nil, and hook output is then
// recorded nowhere.
//
// The argv rows arrive under the names the config doc carries them: a script
// under this machine's config root is root-relative, so it travels, and one
// outside it is machine-local and already absolute. The dispatcher executes
// paths, so every relative argv[0] is resolved against root here -- the last
// point before the hook runs, where a name this machine did not write still
// becomes a path this machine can.
func resolveHooksConfig(hooksMap map[string][][]string, log hooks.RunLog) (hooks.Config, error) {
	root, err := userConfigRoot()
	if err != nil {
		return hooks.Config{}, err
	}
	return hooks.Config{
		Hooks: absolutizeHookArgv(hooksMap, root),
		Log:   log,
	}, nil
}

// absolutizeHookArgv resolves every relative argv[0] against root, leaving the
// arguments after it alone: they are the script's own, and the dispatcher
// passes them through untouched. An argv row that is empty, or whose argv[0] is
// already absolute, is kept as it is.
func absolutizeHookArgv(hooksMap map[string][][]string, root string) map[string][][]string {
	out := make(map[string][][]string, len(hooksMap))
	for event, rows := range hooksMap {
		resolved := make([][]string, 0, len(rows))
		for _, row := range rows {
			if len(row) == 0 || filepath.IsAbs(row[0]) {
				resolved = append(resolved, row)
				continue
			}
			next := make([]string, len(row))
			copy(next, row)
			next[0] = filepath.Join(root, filepath.FromSlash(row[0]))
			resolved = append(resolved, next)
		}
		out[event] = resolved
	}
	return out
}
