package relevo

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/store"
)

// gitShimSource is POSIX sh. Every worktree of one repository shares a single
// stash stack, so a builder's `git stash pop` can take another lane's work.
// A push gets pushInsteadOf rewrites that turn every network URL into an
// unknown scheme, so it fails before connecting: publishing is the
// MasterMind's, and a round that pushed has put unreviewed commits on a PR. A
// push to a local path still runs, because test suites push to temporary bare
// repositories. A remote with an explicit pushurl is not rewritten; git
// applies pushInsteadOf to url only, and insteadOf would break fetch too.
// It finds the subcommand after git's global options (those that take a
// separate value are skipped with it), and finds the real git on PATH while
// skipping its own directory by canonical path, so a duplicate PATH entry or a
// symlinked state directory cannot make it exec itself. It drops its own
// directory from the PATH it hands on: a nested relevo (a test daemon under a
// served round) puts a second shim ahead of this one, and two shims that each
// skip only themselves exec each other forever. exec keeps stdin, arguments,
// output and the exit code untouched.
const gitShimSource = `#!/bin/sh
self_dir=$(cd "$(dirname "$0")" && pwd -P)
self=$self_dir/$(basename "$0")
skip=0
sub=
for a in "$@"; do
	if [ "$skip" = 1 ]; then
		skip=0
		continue
	fi
	case $a in
	-C | -c | --git-dir | --work-tree | --namespace | --config-env | --super-prefix | --attr-source)
		skip=1
		;;
	-*) ;;
	*)
		sub=$a
		break
		;;
	esac
done
if [ "$sub" = stash ]; then
	echo "git stash is refused in a relevo worktree: the stash stack is shared by every worktree of the repository; make a WIP commit or copy files outside the repo instead" >&2
	exit 1
fi
if [ "$sub" = push ]; then
	r=url.relevo-refuses-pushes-from-a-round://.pushInsteadOf
	set -- -c "$r=https://" -c "$r=http://" -c "$r=ssh://" -c "$r=git://" -c "$r=git@" "$@"
fi
set -f
IFS=:
path=
for dir in $PATH; do
	d=$(cd "$dir" 2>/dev/null && pwd -P) && [ "$d" = "$self_dir" ] && continue
	path=${path:+$path:}$dir
done
PATH=$path
export PATH
for dir in $PATH; do
	[ -n "$dir" ] || continue
	d=$(cd "$dir" 2>/dev/null && pwd -P) || continue
	[ "$d" = "$self_dir" ] && continue
	cand=$dir/git
	[ -f "$cand" ] && [ -x "$cand" ] || continue
	[ "$cand" -ef "$self" ] && continue
	exec "$cand" "$@"
done
echo "git: not found on PATH" >&2
exit 127
`

// ensureGitShim writes the shim into dir, replacing it only when its content
// or mode differs. The write goes through a rename so a concurrent round never
// execs a half-written file.
func ensureGitShim(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create git shim dir: %w", err)
	}
	path := filepath.Join(dir, "git")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, []byte(gitShimSource)) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() == 0o755 {
			return nil
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return fmt.Errorf("chmod git shim: %w", err)
		}
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".git-shim-*")
	if err != nil {
		return fmt.Errorf("write git shim: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(gitShimSource); err != nil {
		tmp.Close()
		return fmt.Errorf("write git shim: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write git shim: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return fmt.Errorf("chmod git shim: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("install git shim: %w", err)
	}
	return nil
}

// gitShimPATH puts dir first on path. An empty path yields dir alone: a
// trailing separator would put the working directory on PATH.
func gitShimPATH(path, dir string) string {
	if path == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

// needsGitShim reports whether a binding's round writes to the tree. It keys
// on the binding's shape, which is derived from the role's shape, never on an
// actor name.
func needsGitShim(b store.Binding) bool {
	return b.Shape == store.ShapeWriter
}

// gitShimEnv is the PATH entry a writer round's process is spawned with, nil
// for a round that needs no shim. The shim is in place when it returns.
func gitShimEnv(rt Runtime, b store.Binding) ([]string, error) {
	if !needsGitShim(b) {
		return nil, nil
	}
	dir := rt.Store.GitShimDir()
	if err := ensureGitShim(dir); err != nil {
		return nil, err
	}
	return []string{"PATH=" + gitShimPATH(os.Getenv("PATH"), dir)}, nil
}
