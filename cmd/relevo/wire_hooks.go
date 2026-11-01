package main

import (
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// resolveHooksConfig builds the hook dispatcher environment from the loaded
// hooks section and the run log. A caller with no database open passes nil for
// the log, and hook output is then recorded nowhere. Every relative argv[0]
// is resolved against root here, the last point before the hook runs.
func resolveHooksConfig(hooksMap map[string][][]string, log hooks.RunLog) (hooks.Config, error) {
	root, err := relevo.ConfigRoot()
	if err != nil {
		return hooks.Config{}, err
	}
	return hooks.Config{
		Hooks: absolutizeHookArgv(hooksMap, root),
		Log:   log,
	}, nil
}

// absolutizeHookArgv resolves every relative argv[0] against root and passes the
// remaining arguments through untouched; an empty row, or one already absolute,
// is kept as it is.
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
