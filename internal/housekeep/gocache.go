package housekeep

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// GoCacheDir asks `go env GOCACHE` through run. A host with no go binary
// answers ("", nil): that is an observation about the machine, not a fault of
// the doctor's, so the caller shows an informational row.
func GoCacheDir(ctx context.Context, run func(ctx context.Context, name string, args ...string) ([]byte, error)) (string, error) {
	out, err := run(ctx, "go", "env", "GOCACHE")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GoCacheUsage sizes the Go build cache under the same time and entry budgets
// as the tmp root walk; a truncated total is a floor. A missing directory is
// an empty cache.
func GoCacheUsage(dir string, budget time.Duration, maxEntries int) (bytes int64, truncated bool) {
	u := walkRoot(dir, budget, maxEntries, true)
	return u.Loose, u.Truncated
}

// IsGoTree reports whether dir's root carries a Go module or workspace file.
func IsGoTree(dir string) bool { return isGoTree(dir) }
